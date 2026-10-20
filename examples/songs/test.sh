#!/usr/bin/env bash
# 回归测试：把 cases.json 中的查询批量交给 demo.sh，用 jq 检查第一条结果。
# Regression test: feed all queries in cases.json to demo.sh, check the top result with jq.
#
# 用例格式 / case format: {"cat": "分类", "q": "查询", "expect": ["结果应包含的子串", ...]}
#   expect 为空表示应该没有结果 / empty expect = no result expected
# 输出 Markdown 表格；全部通过时退出码为 0 / prints a Markdown table; exit 0 if all pass
set -euo pipefail
cd "$(dirname "$0")"

jq -r '.[].q' cases.json \
  | ./demo.sh --queries - -o json -n 1 songs.txt \
  | jq -rn --slurpfile cases cases.json '
      [inputs] as $res
      | [range(0; $cases[0] | length) as $i
         | $cases[0][$i] as $c
         | ($res[$i].results[0].record // "") as $top
         | {cat: $c.cat, q: $c.q, top: $top, score: ($res[$i].results[0].score // ""),
            ok: (if ($c.expect | length) == 0 then $top == ""
                 else any($c.expect[]; . as $e | $top | contains($e)) end)}] as $rows
      | "| # | category | query | result | top1 | score |",
        "|---|---|---|---|---|---|",
        ($rows | to_entries[] | .value as $r
          | "| \(.key + 1) | \($r.cat) | \($r.q) | \(if $r.ok then "✅" else "❌" end) | \($r.top) | \($r.score) |"),
        "",
        "| category | passed |", "|---|---|",
        ($rows | group_by(.cat) | sort_by(.[0].cat)[]
          | "| \(.[0].cat) | \(map(select(.ok)) | length)/\(length) |"),
        "| **total** | **\($rows | map(select(.ok)) | length)/\($rows | length)** |",
        (if all($rows[]; .ok) then empty else error("some cases failed") end)'
