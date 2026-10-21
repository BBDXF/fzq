package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const exDir = "../examples/"

// songsCfg 等同 examples/songs/demo.sh 的选项。
func songsCfg() Config {
	return Config{
		MinScore: 65, Limit: 5, Output: "raw", Format: FmtAuto, PrefilterMin: 3000, Candidates: defaultCandidates,
		Extract: `(?:(?P<artist>[^/]+?) - )?(?P<title>[^/]+)\.\w+$`,
		Key:     []string{"title", "artist"},
		Strip: []string{
			`^(下一首|上一首|暂停|播放音乐)$`,
			`^(小爱同学|小度)?[，, ]*(请|帮我)?(播放|放|来|我想听|我要听|想听|听)(一下|一首|首|点)?`,
			`(的歌|吧|呀)$`,
		},
	}
}

func loadEngine(t testing.TB, cfg Config, files ...string) *Engine {
	t.Helper()
	e, err := buildEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rp Path
	if cfg.Records != "" {
		if rp, err = ParsePath(cfg.Records); err != nil {
			t.Fatal(err)
		}
	}
	var recs []*Record
	for _, f := range files {
		r, err := ReadRecords(f, cfg.Format, rp)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r...)
	}
	e.Index(recs)
	return e
}

type simple struct {
	Raw   string
	Line  int
	Score int
	Match string
}

func top(rs []Result) []simple {
	var o []simple
	for _, r := range rs {
		o = append(o, simple{r.Rec.Raw, r.Rec.Line, r.Score, r.Match})
	}
	return o
}

func checkEquiv(t *testing.T, cfg Config, file string, queries []string) {
	t.Helper()
	off, on := cfg, cfg
	off.PrefilterMin, on.PrefilterMin = -1, 0
	a, b := loadEngine(t, off, file), loadEngine(t, on, file)
	if a.post != nil || b.post == nil {
		t.Fatalf("prefilter flags not honored: off.post=%v on.post=%v", a.post != nil, b.post != nil)
	}
	for _, q := range queries {
		x, y := top(a.Search(q, 5)), top(b.Search(q, 5))
		if len(x) != len(y) {
			t.Errorf("%s %q: %d results vs %d with prefilter", file, q, len(x), len(y))
			continue
		}
		for i := range x {
			if x[i] != y[i] {
				t.Errorf("%s %q #%d: %+v vs %+v", file, q, i, x[i], y[i])
			}
		}
	}
}

func TestPrefilterEquivalence(t *testing.T) {
	var cases []struct{ Q string }
	b, err := os.ReadFile(exDir + "songs/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	var qs []string
	for _, c := range cases {
		qs = append(qs, c.Q)
	}
	checkEquiv(t, songsCfg(), exDir+"songs/songs.txt", qs)

	base := Config{MinScore: 65, Format: FmtAuto, PrefilterMin: 3000, Candidates: defaultCandidates}
	checkEquiv(t, base, exDir+"cities.txt", []string{"胡和浩特", "wlmq", "cs", "长沙", "北京", "shanghai", "guangzhou", "不存在"})
	c := base
	c.Key = []string{"name"}
	checkEquiv(t, c, exDir+"contacts.yaml", []string{"张三风", "zsf", "lisisi", "琳达王", "王小明", "linda", "李思思"})
	c = base
	c.Key, c.Records = []string{"name"}, ".data.items[]"
	checkEquiv(t, c, exDir+"products.json", []string{"罗级鼠标", "airpods", "华为手表", "苹果", "无线鼠标"})
}

// ---------------- 召回 ----------------

func firstLetters(ts []Token) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString(firstRune(t.Py))
	}
	return b.String()
}

// perturbed 为每条记录生成若干扰动查询。
func perturbed(e *Engine, rnd *rand.Rand) (qs []string, owner []int) {
	byPy := map[string][]rune{} // 读音 → 曲库里出现过的汉字
	var titles []string
	for _, r := range e.records {
		title := ""
		if v := r.derived["title"]; len(v) > 0 {
			title = v[0]
		}
		titles = append(titles, title)
		for _, tk := range Tokenize(title) {
			if tk.Han {
				byPy[tk.Py] = append(byPy[tk.Py], []rune(tk.Text)[0])
			}
		}
	}
	add := func(i int, q string) {
		if q = strings.TrimSpace(q); q != "" {
			qs, owner = append(qs, q), append(owner, i)
		}
	}
	for i, title := range titles {
		toks := Tokenize(title)
		if len(toks) == 0 {
			continue
		}
		rs := []rune(title)
		// 同音字替换
		for _, p := range rnd.Perm(len(rs)) {
			tk := Tokenize(string(rs[p]))
			if len(tk) != 1 || !tk[0].Han {
				continue
			}
			var alt []rune
			for _, c := range byPy[tk[0].Py] {
				if c != rs[p] {
					alt = append(alt, c)
				}
			}
			if len(alt) > 0 {
				x := append([]rune{}, rs...)
				x[p] = alt[rnd.Intn(len(alt))]
				add(i, string(x))
				break
			}
		}
		if len(rs) >= 4 { // 删掉中间一个字
			p := 1 + rnd.Intn(len(rs)-2)
			add(i, string(rs[:p])+string(rs[p+1:]))
		}
		add(i, joinPy(toks, false)) // 全拼
		if len(toks) >= 2 {         // 单字/单词标题的首字母只有 1 个字符，全量扫描也不会命中
			add(i, firstLetters(toks)) // 首字母
		}
	}
	return
}

func recall(e *Engine, qs []string, owner []int, withFull bool) (hit, relevant, relHit int) {
	for k, q := range qs {
		ids, ok := e.prefilter(e.parseQuery(q))
		in := false
		if !ok {
			in = true
		}
		for _, id := range ids {
			in = in || int(id) == owner[k]
		}
		if in {
			hit++
		} else if withFull && os.Getenv("FZQ_DEBUG") != "" {
			full := *e
			full.PrefilterMin = -1
			rs := full.Search(q, 0)
			sc := -1
			for _, r := range rs {
				if r.Rec == e.records[owner[k]] {
					sc = r.Score
				}
			}
			println("MISS", q, "->", e.records[owner[k]].Raw, "fullscore", sc, "nres", len(rs), "ncand", len(ids))
		}
		if !withFull {
			continue
		}
		// “相关”：全量扫描时目标记录确实能得分
		full := *e
		full.PrefilterMin = -1
		for _, r := range full.Search(q, 0) {
			if r.Rec == e.records[owner[k]] {
				relevant++
				if in {
					relHit++
				}
				break
			}
		}
	}
	return
}

func TestPrefilterRecall(t *testing.T) {
	cfg := songsCfg()
	cfg.PrefilterMin = 0
	e := loadEngine(t, cfg, exDir+"songs/songs.txt")
	qs, owner := perturbed(e, rand.New(rand.NewSource(7)))
	for _, k := range []int{200, 20} {
		e.Candidates = k
		hit, rel, relHit := recall(e, qs, owner, true)
		t.Logf("songs.txt K=%d: %d queries, recall %.2f%% (%d/%d); among %d full-scan-relevant: %.2f%%",
			k, len(qs), 100*float64(hit)/float64(len(qs)), hit, len(qs), rel, 100*float64(relHit)/float64(rel))
		if k == 200 && float64(hit) < 0.995*float64(len(qs)) {
			t.Errorf("recall %d/%d < 99.5%%", hit, len(qs))
		}
	}
}

// FZQ_BENCH_FILE=/tmp/opencode/bench30k.txt：大库上的召回与等价性（慢，默认跳过）。
func TestPrefilterLarge(t *testing.T) {
	f := os.Getenv("FZQ_BENCH_FILE")
	if f == "" {
		t.Skip("FZQ_BENCH_FILE not set")
	}
	cfg := songsCfg()
	if k, err := strconv.Atoi(os.Getenv("FZQ_K")); err == nil {
		cfg.Candidates = k
	}
	e := loadEngine(t, cfg, f)
	if e.post == nil {
		t.Fatal("expected prefilter on a large file")
	}
	rnd := rand.New(rand.NewSource(3))
	qs, owner := perturbed(e, rnd)
	var q2 []string
	var o2 []int
	for i := range qs { // 抽样 3000 条
		if rnd.Intn(len(qs)/3000+1) == 0 {
			q2, o2 = append(q2, qs[i]), append(o2, owner[i])
		}
	}
	hit, _, _ := recall(e, q2, o2, false)
	t.Logf("%d records, K=%d: %d queries, recall %.2f%% (%d/%d)",
		len(e.records), e.Candidates, len(q2), 100*float64(hit)/float64(len(q2)), hit, len(q2))
	full := *e
	full.PrefilterMin = -1
	diff, top1 := 0, 0
	for _, q := range q2[:150] {
		x, y := top(full.Search(q, 5)), top(e.Search(q, 5))
		if len(x) != len(y) {
			diff++
			if len(y) == 0 || x[0] != y[0] {
				top1++
			}
			t.Logf("DIFF %q: %d vs %d results", q, len(x), len(y))
			continue
		}
		for i := range x {
			if x[i] != y[i] {
				diff++
				if i == 0 {
					top1++
				}
				t.Logf("DIFF %q #%d: %+v vs %+v", q, i, x[i], y[i])
				break
			}
		}
	}
	t.Logf("top-5 differs on %d/150 queries (top-1 differs on %d)", diff, top1)
}

// ---------------- 缓存 ----------------

func runCapture(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	so, se := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wo, we
	ch := make(chan string, 2)
	rd := func(f *os.File) {
		var b strings.Builder
		buf := make([]byte, 1<<16)
		for {
			n, err := f.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		ch <- b.String()
	}
	go rd(ro)
	code = run(args)
	os.Stdout, os.Stderr = so, se
	wo.Close()
	out = <-ch
	// 另一个 reader 单独读 stderr
	go rd(re)
	we.Close()
	errOut = <-ch
	return
}

func songArgs(extra ...string) []string {
	c := songsCfg()
	a := []string{"-x", c.Extract, "-k", "title", "-k", "artist"}
	for _, s := range c.Strip {
		a = append(a, "--strip", s)
	}
	return append(a, extra...)
}

func cacheFiles(t *testing.T, dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.gob"))
	return m
}

func TestIndexCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "cache") // 目录不存在时自动创建
	songs := exDir + "songs/songs.txt"
	for _, pm := range []string{"-1", "0"} {
		os.RemoveAll(dir)
		for _, q := range []string{"播放周节伦的琴天", "zjl", "qingtian", "起风了"} {
			args := songArgs("-n", "5", "-o", "text", "--prefilter-min", pm, q, songs)
			want, _, c0 := runCapture(t, args...)
			o1, e1, c1 := runCapture(t, append([]string{"--index-cache", dir}, args...)...)
			o2, e2, c2 := runCapture(t, append([]string{"--index-cache", dir}, args...)...)
			if o1 != want || o2 != want || c1 != c0 || c2 != c0 || e1 != "" || e2 != "" {
				t.Errorf("pm=%s %q: cache output differs\nwant %q\n 1st %q (%q)\n 2nd %q (%q)", pm, q, want, o1, e1, o2, e2)
			}
		}
		if n := len(cacheFiles(t, dir)); n != 1 {
			t.Errorf("want 1 cache file, got %d", n)
		}
	}

	// 第二次确实读缓存：直接加载并与新建的引擎比较
	cfg := songsCfg()
	key, err := cacheKey(&cfg, []string{songs})
	if err != nil {
		t.Fatal(err)
	}
	fresh := loadEngine(t, cfg, songs)
	fresh.WantPost = true
	fresh.Index(fresh.records)
	loaded, _ := buildEngine(&cfg)
	if !loaded.LoadCache(dir, key) {
		t.Fatal("cache not loaded")
	}
	for _, q := range []string{"青花瓷", "zjl", "周杰伦晴天"} {
		x, y := top(fresh.Search(q, 5)), top(loaded.Search(q, 5))
		if len(x) == 0 || len(x) != len(y) {
			t.Fatalf("%q: %d vs %d", q, len(x), len(y))
		}
		for i := range x {
			if x[i] != y[i] {
				t.Errorf("%q #%d: %+v vs %+v", q, i, x[i], y[i])
			}
		}
	}

	// 键：-k / -x / -f / -r / --no-pinyin 变化 → 失效；-n / --min-score / -o 不变
	k := func(mod func(*Config)) string {
		c := songsCfg()
		mod(&c)
		s, _ := cacheKey(&c, []string{songs})
		return s
	}
	for name, mod := range map[string]func(*Config){
		"-k": func(c *Config) { c.Key = []string{"title"} }, "-x": func(c *Config) { c.Extract = `(?P<title>.+)` },
		"-f": func(c *Config) { c.Format = FmtLines }, "-r": func(c *Config) { c.Records = ".a" },
		"--no-pinyin": func(c *Config) { c.NoPinyin = true },
	} {
		if k(mod) == key {
			t.Errorf("changing %s must change the cache key", name)
		}
	}
	for name, mod := range map[string]func(*Config){
		"-n": func(c *Config) { c.Limit = 1 }, "--min-score": func(c *Config) { c.MinScore = 80 },
		"-o": func(c *Config) { c.Output = "json" }, "--strip": func(c *Config) { c.Strip = nil },
		"-t": func(c *Config) { c.Template = "x" },
	} {
		if k(mod) != key {
			t.Errorf("changing %s must not change the cache key", name)
		}
	}
	// 实际行为：换 -k 后得到新的缓存文件且结果正确
	before := len(cacheFiles(t, dir))
	a := []string{"-x", songsCfg().Extract, "-k", "title", "-n", "3", "青花瓷", songs}
	w, _, _ := runCapture(t, a...)
	g, _, _ := runCapture(t, append([]string{"--index-cache", dir}, a...)...)
	if g != w || len(cacheFiles(t, dir)) != before+1 {
		t.Errorf("-k change: output same=%v files %d→%d", g == w, before, len(cacheFiles(t, dir)))
	}

	// 损坏的缓存：一行提示，重建，结果不变
	for _, f := range cacheFiles(t, dir) {
		os.WriteFile(f, []byte("garbage"), 0o644)
	}
	_, e3, _ := runCapture(t, append([]string{"--index-cache", dir}, a...)...)
	g, _, _ = runCapture(t, append([]string{"--index-cache", dir}, a...)...)
	if !strings.Contains(e3, "rebuilding") || strings.Count(e3, "\n") != 1 || g != w {
		t.Errorf("corrupt cache: stderr %q, output ok=%v", e3, g == w)
	}

	// 文件内容变化 → 新键
	src := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(src, []byte("甲乙丙\n丁戊己\n"), 0o644)
	c1, _ := cacheKey(&Config{}, []string{src})
	os.WriteFile(src, []byte("甲乙丙\n丁戊庚\n"), 0o644)
	if c2, _ := cacheKey(&Config{}, []string{src}); c1 == c2 {
		t.Error("content change must change the key")
	}
}

// 模板里的 .record / .fields：缓存前后输出完全一致。
func TestIndexCacheTemplate(t *testing.T) {
	dir := t.TempDir()
	cases := [][]string{
		{"-r", ".data.items[]", "-k", "name", "-t", `{{.record.id}} {{.record.name}} {{.record.price}} {{.line}} {{.source}} {{.score}}`, "airpods", exDir + "products.json"},
		{"-k", "name", "-t", `{{.record}} {{.raw}}`, "zsf", exDir + "contacts.yaml"},
		{"-x", `(?P<a>.)(?P<b>.)`, "-k", "a", "-t", `{{.fields}} {{.record}}`, "北", exDir + "cities.txt"},
		{"-k", "msg", "-o", "json", "链接超时", exDir + "app.jsonl"},
	}
	for _, a := range cases {
		want, _, wc := runCapture(t, a...)
		for i := 0; i < 2; i++ {
			got, e, c := runCapture(t, append([]string{"--index-cache", dir}, a...)...)
			if got != want || c != wc || e != "" || want == "" {
				t.Errorf("%v run %d:\nwant %q\n got %q (%q)", a[len(a)-2:], i, want, got, e)
			}
		}
	}
}

func TestIndexCacheConfigAndStdin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c")
	cf := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cf, []byte("index-cache: "+dir+"\nprefilter-min: 0\ncandidates: 50\n"), 0o644)
	out, _, _ := runCapture(t, "-c", cf, "-k", "name", "zsf", exDir+"contacts.yaml")
	if !strings.Contains(out, "张三丰") || len(cacheFiles(t, dir)) != 1 {
		t.Errorf("config index-cache not honored: %q", out)
	}
	// stdin 不缓存
	os.RemoveAll(dir)
	in, _ := os.Open(exDir + "cities.txt")
	old := os.Stdin
	os.Stdin = in
	out, _, _ = runCapture(t, "--index-cache", dir, "cs")
	os.Stdin = old
	if !strings.Contains(out, "长沙") || len(cacheFiles(t, dir)) != 0 {
		t.Errorf("stdin must not be cached: %q %v", out, cacheFiles(t, dir))
	}
}

// ---------------- 基准 ----------------

func BenchmarkSearch(b *testing.B) {
	f := os.Getenv("FZQ_BENCH_FILE")
	if f == "" {
		b.Skip("FZQ_BENCH_FILE not set")
	}
	for _, pm := range []int{-1, 3000} {
		cfg := songsCfg()
		cfg.PrefilterMin = pm
		e := loadEngine(b, cfg, f)
		name := "full"
		if pm >= 0 {
			name = "prefilter"
		}
		b.Run(name, func(b *testing.B) {
			qs := []string{"晴天", "青花瓷", "zjl", "播放周杰伦的情花", "qingtian"}
			for i := 0; i < b.N; i++ {
				e.Search(qs[i%len(qs)], 5)
			}
		})
	}
}
