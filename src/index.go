package main

import (
	"cmp"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// 倒排预筛选：大曲库下先用廉价的 key 重合度挑出少量候选，再只对候选做完整打分。
// key 种类：s:音节（汉字的所有模糊读音）/ w:英文单词 / d:英文单词删一字 / g:拼音串的 3-gram / i:前 2~4 个字的首字母。

const (
	defaultCandidates = 500
	prefilterMinScore = 60 // 低于此最低分时召回无保证，退回全量扫描
	indexBatch        = 4096
)

// firstRune 字符串的首个字符。
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// appendGrams 追加 s 的 3-gram（不足 3 个字符时用整串）。
func appendGrams(ks []string, s string) []string {
	rs := []rune(s)
	if len(rs) == 0 {
		return ks
	}
	if len(rs) < 3 {
		return append(ks, "g:"+s)
	}
	for i := 0; i+3 <= len(rs); i++ {
		ks = append(ks, "g:"+string(rs[i:i+3]))
	}
	return ks
}

// appendDels 英文单词的“删一个字符”邻域（d:）：编辑距离 ≤1 的拼写错误（skn↔skin）必有共同 key，补足 3-gram 对短词的盲区。
func appendDels(ks []string, w string) []string {
	rs := []rune(w)
	if len(rs) < 2 {
		return ks
	}
	ks = append(ks, "d:"+w)
	if len(rs) < 3 {
		return ks
	}
	for i := range rs {
		ks = append(ks, "d:"+string(rs[:i])+string(rs[i+1:]))
	}
	return ks
}

// maxInitials 首字母 key 的最大长度（前 2..4 个字的首字母）。
const maxInitials = 4

// appendInitials 追加前 m（2..4）个 Token 首字母的所有组合（多音字各读音的首字母都算）。
func appendInitials(ks []string, ts []Token) []string {
	combos := []string{""}
	for m := 1; m <= min(len(ts), maxInitials); m++ {
		var firsts []string
		for _, a := range ts[m-1].Alts {
			firsts = append(firsts, firstRune(a))
		}
		slices.Sort(firsts)
		firsts = slices.Compact(firsts)
		var next []string
		for _, c := range combos {
			for _, f := range firsts {
				next = append(next, c+f)
			}
		}
		if len(next) > 64 {
			break
		}
		combos = next
		if m >= 2 {
			for _, c := range combos {
				ks = append(ks, "i:"+c)
			}
		}
	}
	return ks
}

// pyGrams 追加归一化拼音串与原拼音串的 3-gram。
func pyGrams(ks []string, ts []Token) []string {
	n, p := joinPy(ts, true), joinPy(ts, false)
	ks = appendGrams(ks, n)
	if p != n {
		ks = appendGrams(ks, p)
	}
	return ks
}

// indexKeys 一条记录所有字段变体的 key（去重、有序）。
func indexKeys(toks [][][]Token) []string {
	var ks []string
	for _, f := range toks {
		for _, ts := range f {
			for _, t := range ts {
				if t.Han {
					for _, a := range t.NAlts {
						ks = append(ks, "s:"+a)
					}
				} else {
					ks = append(ks, "w:"+t.Text)
					ks = appendDels(ks, t.Text)
				}
			}
			ks = pyGrams(ks, ts)
			ks = appendInitials(ks, ts)
		}
	}
	slices.Sort(ks)
	return slices.Compact(ks)
}

// segKeys 把英文串按曲库里出现过的音节切分（所有完整切法），返回各音节的 s: key；
// 覆盖拼音输入（meishan）对多音字非首读音、模糊音的匹配，这些 3-gram 覆盖不到。
func (e *Engine) segKeys(w string) []string {
	n := len(w)
	if n < 3 || n > 40 {
		return nil
	}
	var edges [][3]int // 起点、终点（长度 1~6 的音节）
	var key []string
	reach := make([]bool, n+1)
	reach[0] = true
	for i := 0; i < n; i++ {
		if !reach[i] {
			continue
		}
		for j := i + 1; j <= min(n, i+6); j++ {
			k := "s:" + fuzzyPinyin(w[i:j])
			if len(e.post[k]) > 0 {
				edges = append(edges, [3]int{i, j, len(key)})
				key = append(key, k)
				reach[j] = true
			}
		}
	}
	if !reach[n] {
		return nil
	}
	back := make([]bool, n+1) // 能走到末尾
	back[n] = true
	var out []string
	for x := len(edges) - 1; x >= 0; x-- { // 边按起点升序，倒序处理即可
		if back[edges[x][1]] {
			back[edges[x][0]] = true
			out = append(out, key[edges[x][2]])
		}
	}
	return out
}

// queryKeys 查询（所有变体）的 key（去重，顺序确定）。
func (e *Engine) queryKeys(qs [][]Token) []string {
	var ks []string
	for _, q := range qs {
		hasHan := false
		for _, t := range q {
			if t.Han {
				hasHan = true
				for _, a := range t.NAlts {
					ks = append(ks, "s:"+a)
				}
			} else {
				ks = append(ks, "w:"+t.Text)
				ks = appendGrams(ks, t.Text)
				ks = appendDels(ks, t.Text)
				ks = append(ks, e.segKeys(t.Text)...)
			}
		}
		ks = pyGrams(ks, q)
		if rs := []rune(strings.Join(texts(q), "")); !hasHan {
			for m := 2; m <= min(len(rs), maxInitials); m++ {
				ks = append(ks, "i:"+string(rs[:m]))
			}
		}
	}
	seen := make(map[string]bool, len(ks))
	out := ks[:0]
	for _, k := range ks {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (e *Engine) prefilterOn(n int) bool { return e.PrefilterMin >= 0 && n > e.PrefilterMin }

// Index 预处理所有记录（派生字段、取值），并按需建倒排；记录数不超过阈值时保留 Token（小库全量扫描）。
func (e *Engine) Index(recs []*Record) {
	e.defaults()
	e.records, e.post = recs, nil
	usePost := e.prefilterOn(len(recs))
	var post map[string][]int32
	if usePost || e.WantPost {
		post = map[string][]int32{}
	}
	workers := runtime.NumCPU()
	for lo := 0; lo < len(recs); lo += indexBatch {
		hi := min(len(recs), lo+indexBatch)
		keys := make([][]string, hi-lo)
		parallel(hi-lo, workers, func(i int) {
			r := recs[lo+i]
			e.prepare(r)
			toks := e.tokenize(r)
			if post != nil {
				keys[i] = indexKeys(toks)
			}
			if !usePost {
				r.toks = toks // 小库：Token 常驻；大库：用完即弃
			}
		})
		for i, ks := range keys {
			for _, k := range ks {
				post[k] = append(post[k], int32(lo+i))
			}
		}
	}
	if post != nil {
		freeze(post)
		e.post = post
		e.countKeys()
	}
}

// parallel 用 w 个 goroutine 对 0..n-1 调用 f。
func parallel(n, w int, f func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for ; w > 0; w-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				f(i)
			}
		}()
	}
	wg.Wait()
}

// freeze 把各 posting 列表压到同一块连续内存里（省内存、少 GC 对象）。
func freeze(m map[string][]int32) {
	total := 0
	for _, v := range m {
		total += len(v)
	}
	slab := make([]int32, 0, total)
	for k, v := range m {
		s := len(slab)
		slab = append(slab, v...)
		m[k] = slab[s:len(slab):len(slab)]
	}
}

// countKeys 统计每条记录的 key 数（从倒排反推），存 key 数的 1/4 次方。
func (e *Engine) countKeys() {
	cnt := make([]int32, len(e.records))
	for _, l := range e.post {
		for _, id := range l {
			cnt[id]++
		}
	}
	e.nk4 = make([]float32, len(cnt))
	for i, c := range cnt {
		e.nk4[i] = float32(math.Sqrt(math.Sqrt(float64(max(c, 1)))))
	}
}

// prefilter 返回按记录序排列的候选；ok=false 表示不预筛选（全量扫描）。
func (e *Engine) prefilter(qs [][]Token) (ids []int32, ok bool) {
	n := len(e.records)
	if e.post == nil || !e.prefilterOn(n) || e.MinScore < prefilterMinScore {
		return nil, false
	}
	keys := e.queryKeys(qs)
	if len(keys) == 0 {
		return nil, false
	}
	score := make([]float32, n)
	nk4 := e.nk4
	for _, k := range keys {
		l := e.post[k]
		if len(l) == 0 {
			continue
		}
		w := float32(math.Log(1 + float64(n)/float64(len(l)))) // idf
		for _, id := range l {
			if score[id] == 0 {
				ids = append(ids, id)
			}
			score[id] += w
		}
	}
	for _, id := range ids { // 按记录 key 数的 1/4 次方归一化，避免 key 多的长记录靠“碰巧重合”挤占名额
		score[id] /= nk4[id]
	}
	k := e.Candidates
	if k <= 0 {
		k = defaultCandidates
	}
	if len(ids) > k {
		slices.SortFunc(ids, func(a, b int32) int {
			return cmp.Or(cmp.Compare(score[b], score[a]), cmp.Compare(a, b))
		})
		ids = ids[:k]
	}
	slices.Sort(ids) // 按记录序打分，保证同分时顺序与全量扫描一致
	return ids, true
}
