package main

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/agnivade/levenshtein"
	"github.com/mozillazg/go-pinyin"
)

// Token 是匹配的最小单位：一个汉字，或一个连续的字母/数字单词。
type Token struct {
	Text string // 原文（小写）
	Py   string // 全拼（汉字）；单词则等于 Text
	NPy  string // 模糊音归一化后的拼音
	Han  bool
}

var pyArgs = pinyin.NewArgs()

// usePinyin 汉字转拼音（同音字、模糊音、拼音输入、首字母）；--no-pinyin 关闭
var usePinyin = true

// fuzzyPinyin 把方言/ASR 常见混淆音归一化：平翘舌、前后鼻音、n/l、f/h。
func fuzzyPinyin(py string) string {
	s := py
	for _, p := range [][2]string{{"zh", "z"}, {"ch", "c"}, {"sh", "s"}} {
		if strings.HasPrefix(s, p[0]) {
			s = p[1] + s[len(p[0]):]
		}
	}
	if strings.HasPrefix(s, "l") {
		s = "n" + s[1:]
	}
	if strings.HasPrefix(s, "f") {
		s = "h" + s[1:]
		if s == "hei" { // fei→hui（福建等口音 f/h 混读）
			s = "hui"
		}
	}
	for _, p := range [][2]string{{"ing", "in"}, {"eng", "en"}, {"ang", "an"}} {
		if strings.HasSuffix(s, p[0]) {
			s = s[:len(s)-len(p[0])] + p[1]
		}
	}
	return s
}

var (
	cnDigits   = []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}
	digitRunRe = regexp.MustCompile(`[0-9]+`)
)

// numToCN 把 0-9999 的阿拉伯数字读成中文（17→十七, 1999→一九九九 按年份/编号逐位读）。
func numToCN(s string) string {
	if len(s) > 4 || (len(s) > 1 && s[0] == '0') || len(s) == 4 {
		var b strings.Builder
		for _, c := range s {
			b.WriteString(cnDigits[c-'0'])
		}
		return b.String()
	}
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return "零"
	}
	units := []string{"", "十", "百", "千"}
	var out []string
	digits := []int{}
	for x := n; x > 0; x /= 10 {
		digits = append(digits, x%10)
	}
	zero := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if d == 0 {
			zero = true
			continue
		}
		if zero {
			out = append(out, "零")
			zero = false
		}
		if !(d == 1 && i == 1 && len(digits) == 2) { // 十七 而不是 一十七
			out = append(out, cnDigits[d])
		}
		out = append(out, units[i])
	}
	return strings.Join(out, "")
}

// normalize 统一大小写、全角括号、标点，并把数字转成中文读法。
func normalize(s string) string {
	s = strings.NewReplacer("（", "(", "）", ")", "，", ",", "：", ":", "　", " ",
		".", "", "'", "", "’", "", "Ⅱ", "二").Replace(s)
	s = digitRunRe.ReplaceAllStringFunc(s, numToCN)
	return strings.ToLower(s)
}

// Tokenize 将文本切分为 Token 序列。
func Tokenize(s string) []Token {
	s = normalize(s)
	var toks []Token
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			w := word.String()
			toks = append(toks, Token{Text: w, Py: w, NPy: w})
			word.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			py := string(r)
			if usePinyin {
				if p := pinyin.SinglePinyin(r, pyArgs); len(p) > 0 && p[0] != "" {
					py = p[0]
				}
			}
			npy := py
			if usePinyin {
				npy = fuzzyPinyin(py)
			}
			toks = append(toks, Token{Text: string(r), Py: py, NPy: npy, Han: true})
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return toks
}

func joinPy(ts []Token, norm bool) string {
	var b strings.Builder
	for _, t := range ts {
		if norm {
			b.WriteString(t.NPy)
		} else {
			b.WriteString(t.Py)
		}
	}
	return b.String()
}

func initials(ts []Token) string {
	var b strings.Builder
	for _, t := range ts {
		if t.Py != "" && t.Py[0] < 128 {
			b.WriteByte(t.Py[0])
		}
	}
	return b.String()
}

// subCost 两个 Token 的替换代价：0=完全相同 … 1=无关。
func subCost(a, b Token) float64 {
	if a.Text == b.Text {
		return 0
	}
	if a.Han && b.Han {
		switch {
		case a.Py == b.Py && a.Py != "":
			return 0.15 // 同音字
		case a.NPy == b.NPy && a.NPy != "":
			return 0.35 // 模糊音
		}
		return 1
	}
	if !a.Han && !b.Han { // 英文单词拼写错误
		ra, rb := []rune(a.Text), []rune(b.Text)
		n := max(len(ra), len(rb))
		r := float64(levenshtein.ComputeDistance(a.Text, b.Text)) / float64(n)
		if r <= 0.34 {
			return r
		}
	}
	return 1
}

// alignScore 计算查询 q 在目标 t 中的相似度 (0-100)。
// 半全局对齐：q 必须整体对齐，t 的前后缀可以免费跳过（支持“只说半句歌名”），
// 未覆盖的部分以 coverage 系数轻微扣分，使完整匹配排在部分匹配前面。
func alignScore(q, t []Token) float64 {
	m, n := len(q), len(t)
	if m == 0 || n == 0 {
		return 0
	}
	// dp[i][j]: q[:i] 对齐到以 t[j-1] 结尾的子串的最小代价；记录对齐起点以计算覆盖长度
	type cell struct {
		c     float64
		start int
	}
	prev := make([]cell, n+1)
	cur := make([]cell, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = cell{0, j} // 免费跳过 t 的前缀
	}
	for i := 1; i <= m; i++ {
		cur[0] = cell{float64(i), 0}
		for j := 1; j <= n; j++ {
			best := cell{prev[j-1].c + subCost(q[i-1], t[j-1]), prev[j-1].start}
			if v := prev[j].c + 1; v < best.c { // q 多出的字
				best = cell{v, prev[j].start}
			}
			if v := cur[j-1].c + 1; v < best.c { // t 中间缺失的字
				best = cell{v, cur[j-1].start}
			}
			cur[j] = best
		}
		prev, cur = cur, prev
	}
	bestC, span := float64(m), 0
	for j := 1; j <= n; j++ {
		if prev[j].c < bestC || (prev[j].c == bestC && j-prev[j].start > span) {
			bestC, span = prev[j].c, j-prev[j].start
		}
	}
	sim := 1 - bestC/float64(m)
	if sim <= 0 {
		return 0
	}
	coverage := min(1.0, float64(span)/float64(n))
	return 100 * sim * (0.85 + 0.15*coverage)
}

// stringScore 以拼接后的拼音串比较（处理 "qingtian"、"zjl"、"莫吉托→mojito" 这类跨文字输入）。
func stringScore(q, t []Token) (float64, string) {
	qs := strings.Join(texts(q), "")
	if qs == "" {
		return 0, ""
	}
	best, why := 0.0, ""
	try := func(s float64, w string) {
		if s > best {
			best, why = s, w
		}
	}
	isLatin := true
	for _, x := range q {
		if x.Han {
			isLatin = false
		}
	}
	if isLatin && len(qs) >= 2 {
		if in := initials(t); in == qs {
			try(88, "initials")
		} else if strings.HasPrefix(in, qs) && len(qs) >= 3 {
			try(80, "initials-prefix")
		}
	}
	for _, norm := range []bool{false, true} {
		qp, tp := joinPy(q, norm), joinPy(t, norm)
		if len(qp) < 4 || tp == "" {
			continue
		}
		pen := 0.0
		if norm {
			pen = 8
		}
		if qp == tp {
			try(95-pen, "pinyin")
		} else if strings.Contains(tp, qp) && float64(len(qp)) >= 0.5*float64(len(tp)) {
			try(85-pen, "pinyin-contains")
		}
		d := levenshtein.ComputeDistance(qp, tp)
		r := 1 - float64(d)/float64(max(len(qp), len(tp)))
		if r >= 0.75 {
			try(100*r-12-pen, "pinyin-approx")
		}
	}
	return best, why
}

func texts(ts []Token) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Text
	}
	return out
}
