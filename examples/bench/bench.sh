#!/usr/bin/env bash
# 30k 条合成曲库的耗时 / 内存对比（数据生成到 /tmp/opencode，不入库）。
# Time / memory on a 30k-record synthetic library (generated into /tmp/opencode, not committed).
#
#   examples/bench/bench.sh                 # 需要先 go build -o fzq ./src
#   OLD=/path/to/old/fzq examples/bench/bench.sh   # 同时对比旧版本
set -euo pipefail
cd "$(dirname "$0")/../.."
N=${N:-30000}
OUT=${OUT:-/tmp/opencode}
DATA=$OUT/bench$N.txt
CACHE=$OUT/bench-cache
FZQ=${FZQ:-./fzq}
mkdir -p "$OUT"
[[ -f $DATA ]] || go run examples/bench/gen.go -n "$N" -o "$DATA"
rm -rf "$CACHE"

args=(
  -x '(?:(?P<artist>[^/]+?) - )?(?P<title>[^/]+)\.\w+$' -k title -k artist
  --strip '^(下一首|上一首|暂停|播放音乐)$'
  --strip '^(小爱同学|小度)?[，, ]*(请|帮我)?(播放|放|来|我想听|我要听|想听|听)(一下|一首|首|点)?'
  --strip '(的歌|吧|呀)$'
  -n 5
)
Q=${Q:-播放周杰伦的情花}

t() { # 名称 命令...
  local name=$1; shift
  /usr/bin/time -f "$name: %es  %MKB" "$@" >/dev/null 2>"$OUT/time.txt" || true
  tail -n1 "$OUT/time.txt"
}
echo "records: $N   query: $Q"
[[ -n ${OLD:-} ]] && t "old  oneshot          " "$OLD" "${args[@]}" "$Q" "$DATA"
t "new  oneshot, no cache " "$FZQ" "${args[@]}" "$Q" "$DATA"
t "new  oneshot, cache #1 " "$FZQ" "${args[@]}" --index-cache "$CACHE" "$Q" "$DATA"
t "new  oneshot, cache #2 " "$FZQ" "${args[@]}" --index-cache "$CACHE" "$Q" "$DATA"
t "new  full scan (-1)    " "$FZQ" "${args[@]}" --prefilter-min -1 "$Q" "$DATA"

# 常驻：200 条查询（每条的平均耗时）/ long-running: 200 queries
shuf -n 200 --random-source=<(yes) "$DATA" | sed -E 's|.* - (.*)\.flac$|\1|' > "$OUT/queries.txt"
[[ -n ${OLD:-} ]] && t "old  200 queries      " "$OLD" "${args[@]}" --queries "$OUT/queries.txt" "$DATA"
t "new  200 queries, cache" "$FZQ" "${args[@]}" --index-cache "$CACHE" --queries "$OUT/queries.txt" "$DATA"
