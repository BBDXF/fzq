package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// FieldSpec 参与匹配的字段（-k）：--extract 生成的字段名，或 jq 风格路径（.a.b / .tags[]）。
type FieldSpec struct {
	Name string
	Path Path
}

func ParseFieldSpec(s string) (FieldSpec, error) {
	f := FieldSpec{Name: strings.TrimSpace(s)}
	if f.Name == "" {
		f.Name = "."
	}
	var err error
	f.Path, err = ParsePath(f.Name)
	return f, err
}

type Engine struct {
	Fields   []FieldSpec    // 多于一个字段时，查询可以由其中两个字段组合而成（顺序任意）
	Extract  *regexp.Regexp // 命名捕获组 → 字段（作用于记录原文）
	Strip    []*regexp.Regexp
	MinScore float64

	records []*Record
}

const minShortScore = 70 // ≤2 个 Token 的短查询的最低分

type Result struct {
	Rec    *Record
	Score  int
	Match  string // 命中的字段，如 "title" 或 "artist+title"
	Reason string // 打分方式，如 align / pinyin / initials
}

var bracketRe = regexp.MustCompile(`[(（\[【]([^)）\]】]*)[)）\]】]`)

// variants 一个字段值的可匹配形式：原文、去掉括号内容、括号内的内容、拉丁/汉字混合时各自部分。
func variants(v string) []string {
	set := []string{v}
	if m := bracketRe.FindAllStringSubmatch(v, -1); m != nil {
		set = append(set, bracketRe.ReplaceAllString(v, ""))
		for _, x := range m {
			set = append(set, x[1])
		}
	}
	var latin, han strings.Builder
	for _, r := range v {
		if unicode.Is(unicode.Han, r) {
			han.WriteRune(r)
		} else if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			latin.WriteRune(r)
		}
	}
	if latin.Len() > 0 && han.Len() > 0 {
		set = append(set, latin.String(), han.String())
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range set {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) values(r *Record, f FieldSpec) []string {
	if vs, ok := r.derived[f.Name]; ok {
		return vs
	}
	return leafStrings(f.Path.Eval(r.data), nil)
}

// Index 预处理所有记录（派生字段、分词）。
func (e *Engine) Index(recs []*Record) {
	if len(e.Fields) == 0 {
		e.Fields = []FieldSpec{{Name: "."}}
	}
	for _, r := range recs {
		r.derived = map[string][]string{}
		if e.Extract != nil {
			if m := e.Extract.FindStringSubmatch(r.Raw); m != nil {
				for gi, n := range e.Extract.SubexpNames() {
					if v := strings.TrimSpace(m[gi]); n != "" && v != "" {
						r.derived[n] = append(r.derived[n], v)
					}
				}
			}
		}
		r.toks = make([][][]Token, len(e.Fields))
		for fi, f := range e.Fields {
			for _, v := range e.values(r, f) {
				for _, x := range variants(v) {
					if t := Tokenize(x); len(t) > 0 {
						r.toks[fi] = append(r.toks[fi], t)
					}
				}
			}
		}
	}
	e.records = recs
}

// ---------------- 查询 ----------------

// queryVariants 依次应用 --strip；全部被删掉时返回 nil（不搜索）。
// 删减后的查询优先，原句作为后备一起参与匹配，防止误删属于内容本身的字。
func (e *Engine) queryVariants(q string) []string {
	q = strings.TrimSpace(strings.ToLower(q))
	stripped := q
	for _, re := range e.Strip {
		stripped = strings.TrimSpace(re.ReplaceAllString(stripped, ""))
	}
	if stripped == "" {
		return nil
	}
	if stripped != q {
		return []string{stripped, q}
	}
	return []string{q}
}

func tooShort(ts []Token) bool { return len(ts) == 0 || (len(ts) == 1 && ts[0].Han) }

// fieldScore 查询 q 与某字段所有取值的最佳分数（未乘权重）。
func fieldScore(q []Token, cands [][]Token) (float64, string) {
	best, why := 0.0, ""
	for _, c := range cands {
		if v := alignScore(q, c); v > best {
			best, why = v, "align"
		}
		if v, w := stringScore(q, c); v > best {
			best, why = v, w
		}
	}
	return best, why
}

func (e *Engine) scoreRecord(r *Record, q []Token) (float64, string, string) {
	best, match, why := 0.0, "", ""
	for fi, f := range e.Fields {
		v, w := fieldScore(q, r.toks[fi])
		if v > best {
			best, match, why = v, f.Name, w
		}
	}
	if len(e.Fields) < 2 {
		return best, match, why
	}
	// 组合：查询 = 字段 A 的部分 + 字段 B 的部分，A/B 为任意两个不同字段
	for k := 1; k < len(q); k++ {
		left, right := q[:k], q[k:]
		if tooShort(left) || tooShort(right) {
			continue
		}
		for a, fa := range e.Fields {
			sa, _ := fieldScore(left, r.toks[a])
			if sa < 65 {
				continue
			}
			for b, fb := range e.Fields {
				if a == b {
					continue
				}
				sb, _ := fieldScore(right, r.toks[b])
				if sb < 65 {
					continue
				}
				la, lb := float64(len(left)), float64(len(right))
				v := min(100, (sa*la+sb*lb)/(la+lb)+2) // 组合命中奖励 2 分
				if v > best {
					best, match, why = v, fa.Name+"+"+fb.Name, "combine"
				}
			}
		}
	}
	return best, match, why
}

// Search 返回得分最高的 limit 条（limit<=0 表示全部）。
func (e *Engine) Search(query string, limit int) []Result {
	var qs [][]Token
	raw := strings.TrimSpace(strings.ToLower(query))
	for i, v := range e.queryVariants(query) {
		t := Tokenize(v)
		// 剥离后只剩 1 个汉字（如“放生”→“生”）时不采用，只用原句
		if len(t) == 0 || (i == 0 && tooShort(t) && v != raw) {
			continue
		}
		qs = append(qs, t)
	}
	if len(qs) == 0 {
		return nil
	}
	var results []Result
	for _, r := range e.records {
		best, match, why, qlen := 0.0, "", "", 0
		for vi, q := range qs {
			v, m, w := e.scoreRecord(r, q)
			if vi > 0 && len(qs) > 1 {
				v -= 3 // 未剥离的原句略降权
			}
			if v > best {
				best, match, why, qlen = v, m, w, len(q)
			}
		}
		th := e.MinScore
		if qlen <= 2 {
			th = max(th, minShortScore)
		}
		if best >= th && best > 0 {
			results = append(results, Result{r, int(best + 0.5), match, why})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}
