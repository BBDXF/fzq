package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Record 一条待匹配的记录。
type Record struct {
	Raw    string // 输出用的原文：文本行 / 紧凑 JSON（YAML 记录也转成 JSON）
	IsJSON bool   // Raw 是否为 JSON
	Source string // 来源文件名（stdin 为 "-"）
	Line   int    // 文本/JSONL 的行号；JSON/YAML 为记录序号（从 1 开始）

	data    any                 // 解码后的值（文本行为 string）；Index 之后释放，输出时由 Data() 按需还原
	derived map[string][]string // --extract 生成的字段
	fvals   [][]string          // [字段] → 取值（Index 时算好，缓存也只存这个）
	toks    [][][]Token         // [字段][取值变体] → Token 序列；惰性生成（见 Engine.tokens）
}

// Data 返回解码后的记录（模板的 .record）：JSON 记录由紧凑原文还原，文本行为原文。
func (r *Record) Data() any {
	if !r.IsJSON {
		return r.Raw
	}
	dec := json.NewDecoder(strings.NewReader(r.Raw))
	dec.UseNumber()
	var v any
	dec.Decode(&v)
	return v
}

// Format 输入格式。
const (
	FmtAuto  = "auto"
	FmtLines = "lines"
	FmtJSON  = "json"
	FmtJSONL = "jsonl"
	FmtYAML  = "yaml"
)

func detectFormat(name string, head []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return FmtJSON
	case ".jsonl", ".ndjson":
		return FmtJSONL
	case ".yaml", ".yml":
		return FmtYAML
	case ".txt", ".m3u", ".m3u8", ".lst", ".log", ".csv", ".tsv":
		return FmtLines
	}
	// 根据内容嗅探：[ 开头 → JSON；{ 开头 → 多行都以 { 开头视为 JSONL，否则 JSON；--- 开头 → YAML
	t := bytes.TrimSpace(head)
	switch {
	case bytes.HasPrefix(t, []byte("[")):
		return FmtJSON
	case bytes.HasPrefix(t, []byte("{")):
		lines := bytes.Split(t, []byte("\n"))
		if len(lines) > 1 && bytes.HasPrefix(bytes.TrimSpace(lines[1]), []byte("{")) && json.Valid(bytes.TrimSpace(lines[0])) {
			return FmtJSONL
		}
		return FmtJSON
	case bytes.HasPrefix(t, []byte("---")):
		return FmtYAML
	}
	return FmtLines
}

// ReadRecords 读取一个输入源。recordsPath 用于从 JSON/YAML 文档中选出记录（默认：顶层数组的元素，否则整个文档）。
func ReadRecords(name string, format string, recordsPath Path) ([]*Record, error) {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if format == "" || format == FmtAuto {
		head := data
		if len(head) > 4096 {
			head = head[:4096]
		}
		format = detectFormat(name, head)
	}
	switch format {
	case FmtLines:
		return readLines(name, data), nil
	case FmtJSONL:
		return readJSONL(name, data, recordsPath)
	case FmtJSON:
		return selectJSON(name, json.RawMessage(data), recordsPath, 0)
	case FmtYAML:
		return readYAML(name, data, recordsPath)
	}
	return nil, fmt.Errorf("unknown format %q (want auto|lines|json|jsonl|yaml)", format)
}

func readLines(name string, data []byte) []*Record {
	var out []*Record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	n := 0
	for sc.Scan() {
		n++
		l := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, &Record{Raw: l, Source: name, Line: n, data: l})
	}
	return out
}

func readJSONL(name string, data []byte, rp Path) ([]*Record, error) {
	var out []*Record
	for i, l := range bytes.Split(data, []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) == 0 {
			continue
		}
		if !json.Valid(l) {
			return nil, fmt.Errorf("%s line %d: invalid JSON", name, i+1)
		}
		recs, err := selectJSON(name, json.RawMessage(l), rp, i+1)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// selectJSON 从一个 JSON 文档中选出记录。line>0 表示来自 JSONL 的某一行（默认整行即一条记录）。
func selectJSON(name string, doc json.RawMessage, rp Path, line int) ([]*Record, error) {
	var raws []json.RawMessage
	switch {
	case rp != nil:
		r, err := rp.EvalRaw(doc)
		if err != nil {
			return nil, err
		}
		raws = r
	case line > 0:
		raws = []json.RawMessage{doc}
	default:
		t := bytes.TrimSpace(doc)
		if len(t) > 0 && t[0] == '[' {
			if err := json.Unmarshal(t, &raws); err != nil {
				return nil, fmt.Errorf("%s: %v", name, err)
			}
		} else {
			if !json.Valid(t) {
				return nil, fmt.Errorf("%s: invalid JSON", name)
			}
			raws = []json.RawMessage{t}
		}
	}
	out := make([]*Record, 0, len(raws))
	for i, r := range raws {
		var buf bytes.Buffer
		if err := json.Compact(&buf, r); err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		ln := line
		if ln == 0 {
			ln = i + 1
		}
		out = append(out, &Record{Raw: buf.String(), IsJSON: true, Source: name, Line: ln, data: v})
	}
	return out, nil
}

func readYAML(name string, data []byte, rp Path) ([]*Record, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []*Record
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		var b bytes.Buffer
		if err := yamlToJSON(&n, &b); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		recs, err := selectJSON(name, b.Bytes(), rp, 0)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			r.Line = len(out) + 1
			out = append(out, r)
		}
	}
	return out, nil
}

// yamlToJSON 把 YAML 节点按原顺序写成 JSON。
func yamlToJSON(n *yaml.Node, w *bytes.Buffer) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			w.WriteString("null")
			return nil
		}
		return yamlToJSON(n.Content[0], w)
	case yaml.AliasNode:
		return yamlToJSON(n.Alias, w)
	case yaml.SequenceNode:
		w.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := yamlToJSON(c, w); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case yaml.MappingNode:
		w.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				w.WriteByte(',')
			}
			k, _ := json.Marshal(n.Content[i].Value)
			w.Write(k)
			w.WriteByte(':')
			if err := yamlToJSON(n.Content[i+1], w); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			w.WriteString("null")
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return err
			}
			w.WriteString(strconv.FormatBool(b))
		case "!!int", "!!float":
			if json.Valid([]byte(n.Value)) {
				w.WriteString(n.Value)
				return nil
			}
			var f float64
			if err := n.Decode(&f); err != nil {
				return err
			}
			w.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		default:
			s, _ := json.Marshal(n.Value)
			w.Write(s)
		}
	}
	return nil
}
