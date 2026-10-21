package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
)

// 索引缓存：把 Index 的结果（各记录的取值 + 倒排）存到磁盘，下次直接读取，跳过解析与分词。
// 缓存键 = 格式版本 + 算法版本 + 依赖版本 + 影响索引的选项 + 各输入文件名与内容哈希。

const (
	cacheMagic   = "fzq-index"
	cacheVersion = 1 // 文件格式
	indexAlgo    = 1 // key 生成 / 分词 / 取值逻辑变化时递增
)

type cacheHeader struct {
	Magic   string
	Version int
	Key     string
}

type cacheRec struct {
	Raw     string
	IsJSON  bool
	Src     int32
	Line    int32
	Derived map[string][]string
	FVals   [][]string // 来自 derived 的字段存 nil，读取时还原
}

type cacheBody struct {
	Fields  []string
	Sources []string
	Recs    []cacheRec
	PKeys   []string // 倒排：key / 列表长度 / 记录号（列表内差分）
	PLens   []int32
	PIds    []int32
}

// cacheKey 计算缓存键；读不到输入文件时返回错误（交给正常流程报错）。
func cacheKey(cfg *Config, files []string) (string, error) {
	h := sha256.New()
	w := func(s string) { fmt.Fprintf(h, "%d:%s|", len(s), s) }
	w(cacheMagic)
	w(strconv.Itoa(cacheVersion))
	w(strconv.Itoa(indexAlgo))
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			w(d.Path + "@" + d.Version)
		}
	}
	w(cfg.Format)
	w(cfg.Records)
	w(cfg.Extract)
	w(strconv.FormatBool(cfg.NoPinyin))
	w(strconv.Itoa(len(cfg.Key)))
	for _, k := range cfg.Key {
		w(k)
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		fh := sha256.New()
		_, err = io.Copy(fh, f)
		f.Close()
		if err != nil {
			return "", err
		}
		w(name) // Source 出现在输出里，文件名也要进键
		w(hex.EncodeToString(fh.Sum(nil)))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cachePath(dir, key string) string { return filepath.Join(dir, key+".gob") }

// LoadCache 读取缓存并代替 Index；返回是否命中。缓存损坏 / 版本不符时静默重建（仅打一行提示）。
func (e *Engine) LoadCache(dir, key string) bool {
	path := cachePath(dir, key)
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "fzq: index cache %s: %v; rebuilding\n", path, err)
		}
		return false
	}
	defer f.Close()
	if err := e.readCache(bufio.NewReaderSize(f, 1<<20), key); err != nil {
		fmt.Fprintf(os.Stderr, "fzq: index cache %s: %v; rebuilding\n", path, err)
		e.records, e.post = nil, nil
		return false
	}
	return true
}

func (e *Engine) readCache(r io.Reader, key string) error {
	e.defaults()
	dec := gob.NewDecoder(r)
	var hd cacheHeader
	if err := dec.Decode(&hd); err != nil {
		return err
	}
	if hd.Magic != cacheMagic || hd.Version != cacheVersion || hd.Key != key {
		return fmt.Errorf("version mismatch")
	}
	var b cacheBody
	if err := dec.Decode(&b); err != nil {
		return err
	}
	if len(b.Fields) != len(e.Fields) {
		return fmt.Errorf("field mismatch")
	}
	for i, f := range e.Fields {
		if b.Fields[i] != f.Name {
			return fmt.Errorf("field mismatch")
		}
	}
	slab := make([]Record, len(b.Recs))
	recs := make([]*Record, len(b.Recs))
	for i := range b.Recs {
		c := &b.Recs[i]
		if int(c.Src) >= len(b.Sources) {
			return fmt.Errorf("corrupt")
		}
		r := &slab[i]
		*r = Record{Raw: c.Raw, IsJSON: c.IsJSON, Source: b.Sources[c.Src], Line: int(c.Line), derived: c.Derived, fvals: c.FVals}
		if r.derived == nil {
			r.derived = map[string][]string{}
		}
		if len(r.fvals) != len(e.Fields) {
			return fmt.Errorf("corrupt")
		}
		for fi, f := range e.Fields {
			if vs, ok := r.derived[f.Name]; ok {
				r.fvals[fi] = vs
			}
		}
		recs[i] = r
	}
	if len(b.PLens) != len(b.PKeys) {
		return fmt.Errorf("corrupt")
	}
	post := make(map[string][]int32, len(b.PKeys))
	off := 0
	for i, k := range b.PKeys {
		n := int(b.PLens[i])
		if n < 0 || off+n > len(b.PIds) {
			return fmt.Errorf("corrupt")
		}
		l := b.PIds[off : off+n : off+n]
		off += n
		prev := int32(0)
		for j, d := range l { // 还原差分
			prev += d
			l[j] = prev
		}
		if n > 0 && (l[0] < 0 || int(l[n-1]) >= len(recs)) {
			return fmt.Errorf("corrupt")
		}
		post[k] = l
	}
	e.records, e.post = recs, post
	e.countKeys()
	return nil
}

// SaveCache 写缓存：先写临时文件再 rename。
func (e *Engine) SaveCache(dir, key string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 成功后已不存在
	bw := bufio.NewWriterSize(tmp, 1<<20)
	err = e.writeCache(bw, key)
	if err == nil {
		err = bw.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), cachePath(dir, key))
}

func (e *Engine) writeCache(w io.Writer, key string) error {
	b := cacheBody{Recs: make([]cacheRec, len(e.records))}
	for _, f := range e.Fields {
		b.Fields = append(b.Fields, f.Name)
	}
	srcs := map[string]int32{}
	for i, r := range e.records {
		si, ok := srcs[r.Source]
		if !ok {
			si = int32(len(b.Sources))
			srcs[r.Source] = si
			b.Sources = append(b.Sources, r.Source)
		}
		fv := make([][]string, len(e.Fields))
		for fi, f := range e.Fields {
			if _, ok := r.derived[f.Name]; !ok {
				fv[fi] = r.fvals[fi]
			}
		}
		b.Recs[i] = cacheRec{Raw: r.Raw, IsJSON: r.IsJSON, Src: si, Line: int32(r.Line), Derived: r.derived, FVals: fv}
	}
	keys := make([]string, 0, len(e.post))
	for k := range e.post {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	b.PKeys = keys
	for _, k := range keys {
		l := e.post[k]
		b.PLens = append(b.PLens, int32(len(l)))
		prev := int32(0)
		for _, id := range l {
			b.PIds = append(b.PIds, id-prev)
			prev = id
		}
	}
	enc := gob.NewEncoder(w)
	if err := enc.Encode(cacheHeader{cacheMagic, cacheVersion, key}); err != nil {
		return err
	}
	return enc.Encode(&b)
}
