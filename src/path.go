package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 路径是 jq 语法的一个子集：
//
//	.            整个值
//	.a.b         对象取键
//	.a[]         展开数组（或对象的所有值）
//	.a[0]        数组下标（支持负数）
//	.["a b"]  ."a b"   含特殊字符的键
//	a.b          省略开头的点
type step struct {
	kind  byte // 'k' 键, 'i' 下标, 'e' 展开
	key   string
	index int
}

type Path []step

func ParsePath(s string) (Path, error) {
	s = strings.TrimSpace(s)
	var p Path
	i := 0
	if strings.HasPrefix(s, ".") {
		i = 1
	}
	for i < len(s) {
		switch {
		case s[i] == '.':
			i++
		case s[i] == '[':
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("path %q: missing ]", s)
			}
			inner := strings.TrimSpace(s[i+1 : i+end])
			i += end + 1
			switch {
			case inner == "":
				p = append(p, step{kind: 'e'})
			case inner[0] == '"':
				k, err := strconv.Unquote(inner)
				if err != nil {
					return nil, fmt.Errorf("path %q: %v", s, err)
				}
				p = append(p, step{kind: 'k', key: k})
			default:
				n, err := strconv.Atoi(inner)
				if err != nil {
					return nil, fmt.Errorf("path %q: invalid index %q", s, inner)
				}
				p = append(p, step{kind: 'i', index: n})
			}
		case s[i] == '"':
			j := i + 1
			for j < len(s) && (s[j] != '"' || s[j-1] == '\\') {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("path %q: unterminated quote", s)
			}
			k, err := strconv.Unquote(s[i : j+1])
			if err != nil {
				return nil, fmt.Errorf("path %q: %v", s, err)
			}
			p = append(p, step{kind: 'k', key: k})
			i = j + 1
		default:
			j := i
			for j < len(s) && s[j] != '.' && s[j] != '[' {
				j++
			}
			p = append(p, step{kind: 'k', key: s[i:j]})
			i = j
		}
	}
	return p, nil
}

// Eval 在已解码的值（map[string]any / []any / 标量）上求值。
func (p Path) Eval(v any) []any {
	cur := []any{v}
	for _, st := range p {
		var next []any
		for _, c := range cur {
			switch st.kind {
			case 'k':
				if m, ok := c.(map[string]any); ok {
					if x, ok := m[st.key]; ok {
						next = append(next, x)
					}
				}
			case 'i':
				if a, ok := c.([]any); ok {
					i := st.index
					if i < 0 {
						i += len(a)
					}
					if i >= 0 && i < len(a) {
						next = append(next, a[i])
					}
				}
			case 'e':
				switch x := c.(type) {
				case []any:
					next = append(next, x...)
				case map[string]any:
					for _, y := range x {
						next = append(next, y)
					}
				}
			}
		}
		cur = next
	}
	return cur
}

// EvalRaw 在原始 JSON 上求值，返回命中的原始 JSON 片段（保持键顺序与原文）。
func (p Path) EvalRaw(raw json.RawMessage) ([]json.RawMessage, error) {
	cur := []json.RawMessage{raw}
	for _, st := range p {
		var next []json.RawMessage
		for _, c := range cur {
			switch st.kind {
			case 'k':
				var m map[string]json.RawMessage
				if json.Unmarshal(c, &m) == nil {
					if x, ok := m[st.key]; ok {
						next = append(next, x)
					}
				}
			case 'i', 'e':
				var a []json.RawMessage
				if err := json.Unmarshal(c, &a); err != nil {
					if st.kind == 'e' { // 对象展开：按原文顺序
						vals, err2 := orderedObjectValues(c)
						if err2 == nil {
							next = append(next, vals...)
						}
					}
					continue
				}
				if st.kind == 'e' {
					next = append(next, a...)
				} else {
					i := st.index
					if i < 0 {
						i += len(a)
					}
					if i >= 0 && i < len(a) {
						next = append(next, a[i])
					}
				}
			}
		}
		cur = next
	}
	return cur, nil
}

func orderedObjectValues(raw json.RawMessage) ([]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not object")
	}
	var out []json.RawMessage
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// leafStrings 收集值中所有标量叶子，转成字符串（对象/数组递归展开）。
func leafStrings(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		if x != "" {
			out = append(out, x)
		}
	case json.Number:
		out = append(out, x.String())
	case bool:
		out = append(out, strconv.FormatBool(x))
	case float64:
		out = append(out, strconv.FormatFloat(x, 'f', -1, 64))
	case []any:
		for _, y := range x {
			out = leafStrings(y, out)
		}
	case map[string]any:
		for _, y := range x {
			out = leafStrings(y, out)
		}
	}
	return out
}
