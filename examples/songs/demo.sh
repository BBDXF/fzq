#!/usr/bin/env bash
# 语音点歌示例：只用 fzq 的通用参数。
# Voice song search demo using only generic fzq options.
#
#   ./demo.sh '播放周节伦的琴天'
#   ./demo.sh 起风了 -o json
#   ./test.sh                             # 回归测试 / regression test
set -euo pipefail
cd "$(dirname "$0")"
FZQ=${FZQ:-../../fzq}

args=(
  # 从路径中取出 artist（可选）和 title
  # Extract artist (optional) and title from the path
  -x '(?:(?P<artist>[^/]+?) - )?(?P<title>[^/]+)\.\w+$'

  # 按歌名、歌手匹配；两个字段时也能匹配“歌手+歌名”的组合
  # Match title and artist; with two keys "artist + title" queries also work
  -k title -k artist

  # 去掉口语。第一条整句删除，即播放控制指令不返回结果
  # Remove filler. The first rule deletes the whole query, so playback commands return nothing
  --strip '^(下一首|上一首|暂停|播放音乐)$'
  --strip '^(小爱同学|小度)?[，, ]*(请|帮我)?(播放|放|来|我想听|我要听|想听|听)(一下|一首|首|点)?'
  --strip '(的歌|吧|呀)$'
)

# 从管道输入时读取管道中的曲库，否则使用 songs.txt
# Read the library from a pipe if there is one, otherwise use songs.txt
if [[ ! -p /dev/stdin ]]; then set -- "$@" songs.txt; fi
exec "$FZQ" "${args[@]}" -n 5 "$@"
