package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// version 在发布时由 -ldflags "-X main.version=v1.2.3" 注入
// version is injected at release time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `fzq - fuzzy matcher for spoken / voice-style queries
      (Chinese homophones, pinyin, fuzzy pinyin, typos, partial input)

Usage:
  fzq [options] QUERY [FILE...]            read stdin when no FILE is given
  fzq [options] --queries Q.txt [FILE...]  batch: one query per line ("-" = stdin, long-running)

Input:
  -f, --format FMT      auto|lines|json|jsonl|yaml (default auto)
  -r, --records PATH    where the records are in a JSON/YAML document, e.g. .data.items[]
                        (default: elements of a top-level array, otherwise the whole document)

Matching:
  -k, --key FIELD       field to match, repeatable: a path (.name, .tags[]) or an --extract
                        group name (default: the whole record). With several keys, a query
                        may also be split across two of them, in any order.
  -x, --extract REGEX   create fields from the named groups of REGEX, applied to each record's
                        text, e.g. '(?P<artist>.+) - (?P<title>.+)\.mp3$'
      --strip REGEX     remove matches of REGEX from the query, repeatable, applied in order;
                        if nothing is left, the query returns no result
      --min-score N     minimum score 0-100 (default 65)
      --no-pinyin       match by characters only (no homophones / pinyin input)

Output:
  -n, --limit N         max results (default 10; 0 = all)
  -o, --output FMT      raw|text|json|jsonl (default raw: print matched records as-is)
  -t, --template TPL    Go template per result, e.g. '{{.score}}\t{{.record.title}}'

Performance (large libraries):
      --prefilter-min N   use the inverted-index prefilter above N records (default 3000;
                          -1 = never, always score every record)
      --candidates K      candidates kept by the prefilter (default 500)
      --index-cache DIR   cache the built index in DIR, keyed by input content and index
                          options (not for stdin input)

Other:
  -c, --config FILE     load options from YAML (keys = long option names)

Exit status: 0 match found, 1 no match, 2 error
`

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

// Config 所有选项；可来自 YAML 配置文件，命令行覆盖之（--strip 追加，-k 替换）。
type Config struct {
	Format   string   `yaml:"format"`
	Records  string   `yaml:"records"`
	Key      []string `yaml:"key"`
	Extract  string   `yaml:"extract"`
	Strip    []string `yaml:"strip"`
	MinScore float64  `yaml:"min-score"`
	NoPinyin bool     `yaml:"no-pinyin"`
	Limit    int      `yaml:"limit"`
	Output   string   `yaml:"output"`
	Template string   `yaml:"template"`

	PrefilterMin int    `yaml:"prefilter-min"`
	Candidates   int    `yaml:"candidates"`
	IndexCache   string `yaml:"index-cache"`

	Queries string `yaml:"-"`
}

func main() { os.Exit(run(os.Args[1:])) }

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "fzq: "+format+"\n", a...)
	return 2
}

func run(args []string) int {
	cfg := Config{MinScore: 65, Limit: 10, Output: "raw", Format: FmtAuto, PrefilterMin: 3000, Candidates: defaultCandidates}
	var cli Config
	var keys, strip listFlag
	var cfgFile string
	fs := flag.NewFlagSet("fzq", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, "fzq %s\n%s", version, usage) }
	str := func(p *string, names ...string) {
		for _, n := range names {
			fs.StringVar(p, n, "", "")
		}
	}
	for _, n := range []string{"k", "key"} {
		fs.Var(&keys, n, "")
	}
	str(&cli.Format, "f", "format")
	str(&cli.Records, "r", "records")
	str(&cli.Extract, "x", "extract")
	fs.Var(&strip, "strip", "")
	fs.Float64Var(&cli.MinScore, "min-score", 0, "")
	fs.BoolVar(&cli.NoPinyin, "no-pinyin", false, "")
	fs.IntVar(&cli.Limit, "n", 0, "")
	fs.IntVar(&cli.Limit, "limit", 0, "")
	str(&cli.Output, "o", "output")
	str(&cli.Template, "t", "template")
	str(&cfgFile, "c", "config")
	str(&cli.Queries, "queries")
	fs.IntVar(&cli.PrefilterMin, "prefilter-min", 0, "")
	fs.IntVar(&cli.Candidates, "candidates", 0, "")
	str(&cli.IndexCache, "index-cache")

	// 允许选项与位置参数交错（fzq '查询' file -n 3）
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}

	if cfgFile != "" {
		b, err := os.ReadFile(cfgFile)
		if err != nil {
			return fail("%v", err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return fail("config %s: %v", cfgFile, err)
		}
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	pick := func(names ...string) bool { return set[names[0]] || (len(names) > 1 && set[names[1]]) }
	if pick("f", "format") {
		cfg.Format = cli.Format
	}
	if pick("r", "records") {
		cfg.Records = cli.Records
	}
	if pick("x", "extract") {
		cfg.Extract = cli.Extract
	}
	if len(keys) > 0 {
		cfg.Key = keys
	}
	cfg.Strip = append(cfg.Strip, strip...)
	cfg.NoPinyin = cfg.NoPinyin || cli.NoPinyin
	if pick("min-score") {
		cfg.MinScore = cli.MinScore
	}
	if pick("n", "limit") {
		cfg.Limit = cli.Limit
	}
	if pick("o", "output") {
		cfg.Output = cli.Output
	}
	if pick("t", "template") {
		cfg.Template = cli.Template
	}
	if pick("prefilter-min") {
		cfg.PrefilterMin = cli.PrefilterMin
	}
	if pick("candidates") {
		cfg.Candidates = cli.Candidates
	}
	if pick("index-cache") {
		cfg.IndexCache = cli.IndexCache
	}
	cfg.Queries = cli.Queries

	eng, err := buildEngine(&cfg)
	if err != nil {
		return fail("%v", err)
	}

	query := ""
	if cfg.Queries == "" {
		if len(pos) == 0 {
			fs.Usage()
			return 2
		}
		query, pos = pos[0], pos[1:]
	}
	if len(pos) == 0 {
		if cfg.Queries == "-" {
			return fail("--queries - reads queries from stdin; pass data as file arguments")
		}
		pos = []string{"-"}
	}
	var rp Path
	if cfg.Records != "" {
		if rp, err = ParsePath(cfg.Records); err != nil {
			return fail("%v", err)
		}
	}
	// 索引缓存：键取决于输入内容与索引选项；stdin 不缓存
	ckey := ""
	if cfg.IndexCache != "" && !slices.Contains(pos, "-") {
		ckey, _ = cacheKey(&cfg, pos) // 读不到文件时 ckey 为空，由下面的 ReadRecords 报错
	}
	if ckey == "" || !eng.LoadCache(cfg.IndexCache, ckey) {
		var recs []*Record
		for _, f := range pos {
			r, err := ReadRecords(f, cfg.Format, rp)
			if err != nil {
				return fail("%v", err)
			}
			recs = append(recs, r...)
		}
		eng.WantPost = ckey != ""
		eng.Index(recs)
		if ckey != "" {
			if err := eng.SaveCache(cfg.IndexCache, ckey); err != nil {
				fmt.Fprintf(os.Stderr, "fzq: index cache: %v\n", err)
			}
		}
	}

	p := &Printer{Format: cfg.Output}
	if cfg.Template != "" {
		t, err := template.New("t").Option("missingkey=zero").Parse(strings.ReplaceAll(cfg.Template, `\t`, "\t"))
		if err != nil {
			return fail("template: %v", err)
		}
		p.Format, p.Tmpl = "template", t
	}

	if cfg.Queries != "" {
		p.Multi = true
		in := os.Stdin
		if cfg.Queries != "-" {
			f, err := os.Open(cfg.Queries)
			if err != nil {
				return fail("%v", err)
			}
			defer f.Close()
			in = f
		}
		if err := runQueries(eng, in, cfg.Limit, p); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	rs := eng.Search(query, cfg.Limit)
	if err := p.Print(os.Stdout, query, rs); err != nil {
		return fail("%v", err)
	}
	if len(rs) == 0 {
		return 1
	}
	return 0
}

func buildEngine(cfg *Config) (*Engine, error) {
	usePinyin = !cfg.NoPinyin
	e := &Engine{MinScore: cfg.MinScore, PrefilterMin: cfg.PrefilterMin, Candidates: cfg.Candidates}
	for _, k := range cfg.Key {
		f, err := ParseFieldSpec(k)
		if err != nil {
			return nil, err
		}
		e.Fields = append(e.Fields, f)
	}
	if cfg.Extract != "" {
		x, err := regexp.Compile(cfg.Extract)
		if err != nil {
			return nil, fmt.Errorf("--extract: %v", err)
		}
		e.Extract = x
	}
	for _, s := range cfg.Strip {
		x, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("--strip %q: %v", s, err)
		}
		e.Strip = append(e.Strip, x)
	}
	return e, nil
}
