//go:build ignore

// 生成 N 条标题互不相同的合成曲库（字取自 songs.txt 的歌名/歌手，保持字频分布）。
// Generate a synthetic library of N songs with DISTINCT titles (chars sampled from songs.txt).
//
//	go run examples/bench/gen.go -n 30000 -o /tmp/opencode/bench30k.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"unicode"
)

func main() {
	n := flag.Int("n", 30000, "records")
	src := flag.String("src", "examples/songs/songs.txt", "seed file")
	out := flag.String("o", "/tmp/opencode/bench30k.txt", "output")
	flag.Parse()

	re := regexp.MustCompile(`([^/]+?) - ([^/]+)\.\w+$`)
	var tc, ac []rune // 歌名 / 歌手用字（保留频率）
	var words []string
	f, err := os.Open(*src)
	if err != nil {
		panic(err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := re.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		for _, r := range m[2] {
			if unicode.Is(unicode.Han, r) {
				tc = append(tc, r)
			}
		}
		for _, r := range m[1] {
			if unicode.Is(unicode.Han, r) {
				ac = append(ac, r)
			}
		}
		for _, w := range strings.Fields(m[2]) {
			if w[0] < 128 {
				words = append(words, w)
			}
		}
	}
	rnd := rand.New(rand.NewSource(1))
	pick := func(pool []rune, k int) string {
		var b strings.Builder
		for i := 0; i < k; i++ {
			b.WriteRune(pool[rnd.Intn(len(pool))])
		}
		return b.String()
	}
	o, _ := os.Create(*out)
	w := bufio.NewWriter(o)
	seen := map[string]bool{}
	for len(seen) < *n {
		title := pick(tc, 2+rnd.Intn(7))
		if rnd.Intn(10) == 0 && len(words) > 0 {
			title = words[rnd.Intn(len(words))] + " " + pick(tc, 1+rnd.Intn(3))
		}
		if seen[title] {
			continue
		}
		seen[title] = true
		fmt.Fprintf(w, "D:/music/bench/%s - %s.flac\n", pick(ac, 2+rnd.Intn(3)), title)
	}
	w.Flush()
	o.Close()
}
