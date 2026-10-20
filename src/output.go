package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/template"
)

// JSONResult 单条结果（-o json / jsonl）。
type JSONResult struct {
	Rank   int             `json:"rank"`
	Score  int             `json:"score"`
	Match  string          `json:"match"`
	Reason string          `json:"reason"`
	Source string          `json:"source"`
	Line   int             `json:"line"`
	Record json.RawMessage `json:"record"` // JSON 记录原样嵌入；文本行为字符串
}

// JSONResponse 一次查询（-o json）。ambiguous: 第 1、2 名分差 < 5，调用方宜让用户确认。
type JSONResponse struct {
	Query     string       `json:"query"`
	Count     int          `json:"count"`
	Ambiguous bool         `json:"ambiguous"`
	Results   []JSONResult `json:"results"`
}

func toJSONResult(i int, r Result) JSONResult {
	rec := json.RawMessage(r.Rec.Raw)
	if !r.Rec.IsJSON {
		b, _ := json.Marshal(r.Rec.Raw)
		rec = b
	}
	return JSONResult{Rank: i + 1, Score: r.Score, Match: r.Match, Reason: r.Reason,
		Source: r.Rec.Source, Line: r.Rec.Line, Record: rec}
}

type Printer struct {
	Format string // raw | json | jsonl | text | template
	Tmpl   *template.Template
	Multi  bool // 多查询模式：raw/text 输出后追加分隔
}

func (p *Printer) Print(w io.Writer, q string, rs []Result) error {
	switch p.Format {
	case "raw":
		for _, r := range rs {
			fmt.Fprintln(w, r.Rec.Raw)
		}
		if p.Multi {
			fmt.Fprintln(w) // 空行结束本条查询
		}
	case "text":
		for _, r := range rs {
			fmt.Fprintf(w, "%3d  %-16s %s\n", r.Score, r.Match+"/"+r.Reason, r.Rec.Raw)
		}
		if p.Multi {
			fmt.Fprintln(w, "---")
		}
	case "jsonl":
		for i, r := range rs {
			b, _ := json.Marshal(toJSONResult(i, r))
			fmt.Fprintln(w, string(b))
		}
		if p.Multi {
			fmt.Fprintln(w)
		}
	case "json":
		resp := JSONResponse{Query: q, Count: len(rs), Results: []JSONResult{}}
		for i, r := range rs {
			resp.Results = append(resp.Results, toJSONResult(i, r))
		}
		resp.Ambiguous = len(rs) > 1 && rs[0].Score-rs[1].Score < 5
		b, _ := json.Marshal(resp)
		fmt.Fprintln(w, string(b))
	case "template":
		for i, r := range rs {
			var rec any = r.Rec.Raw
			if r.Rec.IsJSON {
				rec = r.Rec.data
			}
			fields := map[string][]string{}
			for k, v := range r.Rec.derived {
				fields[k] = v
			}
			err := p.Tmpl.Execute(w, map[string]any{
				"rank": i + 1, "score": r.Score, "match": r.Match, "reason": r.Reason,
				"source": r.Rec.Source, "line": r.Rec.Line, "raw": r.Rec.Raw,
				"record": rec, "fields": fields, "query": q,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(w)
		}
		if p.Multi {
			fmt.Fprintln(w)
		}
	default:
		return fmt.Errorf("unknown output format %q (want raw|text|json|jsonl)", p.Format)
	}
	return nil
}

// runQueries 批量/常驻模式：从 r 逐行读取查询并逐条输出（每条后立即 flush）。
func runQueries(e *Engine, r io.Reader, limit int, p *Printer) error {
	sc := bufio.NewScanner(r)
	w := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		q := strings.TrimSpace(sc.Text())
		if q == "" {
			continue
		}
		if err := p.Print(w, q, e.Search(q, limit)); err != nil {
			return err
		}
		w.Flush()
	}
	return sc.Err()
}
