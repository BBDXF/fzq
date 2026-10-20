# fzq

面向口语输入的模糊匹配工具：从文本 / JSON / JSONL / YAML / stdin 读取记录，用一句可能带错字、同音字、拼音或口语的查询去匹配，按分数输出记录。

A fuzzy matcher for spoken queries: reads records from text / JSON / JSONL / YAML / stdin, matches them against a query that may contain typos, Chinese homophones, pinyin or filler words, and prints the best matches.

```bash
fzq 张三风 examples/contacts.yaml       # 同音字 / homophone → {"name":"张三丰",...}
fzq -k name zsf examples/contacts.yaml  # 首字母 / initials  → 张三丰
ls src | fzq engin                      # 文本行 / text lines → engine.go
```

能处理：同音字、多音字（长沙 = changsha / cs）、错字、拼音、首字母、方言模糊音（平翘舌、前后鼻音、n/l、f/h）、只说一部分或省略中间的词（华为手表 → 华为智能手表）、英文拼错、数字读法（十七 ⇄ 17）、两个字段任意顺序组合。
Handles homophones, polyphonic characters (长沙 = changsha / cs), typos, pinyin, initials, fuzzy pinyin, partial input or skipped words (华为手表 → 华为智能手表), English typos, number forms (十七 ⇄ 17), and queries combining two fields in any order.

## 编译 / Build

```bash
go build -o fzq ./src
```

## 参数 / Options

```
fzq [options] QUERY [FILE...]             没有文件时读 stdin / read stdin when no FILE
fzq [options] --queries Q.txt [FILE...]   每行一条查询，"-" 为 stdin / one query per line
```

| 参数 / Option | 说明 / Description |
|---|---|
| `-f, --format` | `auto`（默认 / default）`lines` `json` `jsonl` `yaml` |
| `-r, --records PATH` | 记录在 JSON/YAML 中的位置，如 `.data.items[]` / where records are, e.g. `.data.items[]` |
| `-k, --key FIELD` | 参与匹配的字段，可重复；多个字段时查询可由其中两个组合 / field to match, repeatable; a query may combine two keys |
| `-x, --extract REGEX` | 用命名捕获组从记录原文取出字段 / create fields from named groups |
| `--strip REGEX` | 从查询中删除，可重复；删空则无结果 / remove from the query, repeatable; empty query → no result |
| `--min-score N` | 最低分，默认 65 / minimum score, default 65 |
| `--no-pinyin` | 关闭拼音匹配 / disable pinyin matching |
| `-n, --limit N` | 最多结果数，默认 10，0 为全部 / max results, default 10, 0 = all |
| `-o, --output` | `raw`（原样输出记录，默认 / records as-is, default）`text` `json` `jsonl` |
| `-t, --template TPL` | Go 模板 / Go template: `.score .match .reason .record .fields .raw .line .source` |
| `-c, --config FILE` | 从 YAML 读取选项，键名同长选项 / load options from YAML |

- 路径 / Paths：jq 子集 / jq subset — `.a.b`、`.a[]`、`.a[0]`、`."a b"`。
- 退出码 / Exit status：`0` 有结果 / match，`1` 无结果 / no match，`2` 出错 / error。
- `-o json`：`{"query","count","ambiguous","results":[{"rank","score","match","reason","source","line","record"}]}`；`ambiguous` 表示前两名分差 < 5 / top-2 scores differ by < 5。

## 示例 / Examples

`examples/` 中附带了示例数据，可以直接运行（在项目根目录执行）：
Sample data is included in `examples/`; run these from the project root:

| 文件 / File | 内容 / Content |
|---|---|
| `contacts.yaml` | 通讯录 / contacts (YAML) |
| `products.json` | 商品，嵌套在 `.data.items` 中 / products nested in `.data.items` (JSON) |
| `app.jsonl` | 日志 / logs (JSONL) |
| `cities.txt` | 城市名，每行一个 / one city per line (text) |
| `songs/songs.txt` | 569 首歌的文件路径 / 569 song file paths |

```bash
# 文本：方言音、首字母 / text: accent, initials
$ fzq 胡和浩特 examples/cities.txt
呼和浩特
$ fzq wlmq examples/cities.txt
乌鲁木齐
$ fzq cs examples/cities.txt          # 多音字 / polyphone: 长 = chang
长沙

# 省略中间的词 / skipped words
$ fzq -r '.data.items[]' -k name 罗级鼠标 examples/products.json
{"id":4,"name":"罗技无线鼠标 MX Master 3S","price":699}

# YAML：拼音输入，文本输出 / YAML: pinyin input, text output
$ fzq -k name -o text lisisi examples/contacts.yaml
 95  name/pinyin      {"name":"李思思","dept":"市场部","phone":"13800000002"}

# 中文读英文名 / English name spoken in Chinese
$ fzq -k name 琳达王 examples/contacts.yaml
{"name":"Linda Wang","dept":"市场部","phone":"13800000005"}

# 两个字段组合 / two fields combined
$ fzq -k name -k dept 研发部的王晓明 examples/contacts.yaml
{"name":"王小明","dept":"研发部","tags":["前端","实习"]}

# 嵌套 JSON + 模板 / nested JSON + template
$ fzq -r '.data.items[]' -k name airpods examples/products.json -t '{{.record.id}}\t{{.record.name}}\t{{.score}}'
2	苹果 AirPods Pro	89

# JSONL 日志，先用 jq 过滤 / JSONL logs, filtered with jq first
$ jq -c 'select(.level=="error")' examples/app.jsonl | fzq -k msg 链接超时
{"time":"10:01","level":"error","msg":"数据库连接超时"}

# 常驻：逐行查询 / long-running: one query per line
$ printf 'zsf\n不存在的人\n' | fzq --queries - -k name -o json examples/contacts.yaml
{"query":"zsf","count":1,"ambiguous":false,"results":[{"rank":1,"score":88,"match":"name","reason":"initials",…}]}
{"query":"不存在的人","count":0,"ambiguous":false,"results":[]}
```

## 语音点歌 / Voice song search

`examples/songs/songs.txt` 是 569 个文件路径。只用通用参数：
`examples/songs/songs.txt` holds 569 file paths. Generic options only:

```bash
fzq -x '(?:(?P<artist>[^/]+?) - )?(?P<title>[^/]+)\.\w+$' \
    -k title -k artist \
    --strip '^(下一首|上一首|暂停|播放音乐)$' \
    --strip '^(小爱同学|小度)?[，, ]*(请|帮我)?(播放|放|来|我想听|我要听|想听|听)(一下|一首|首|点)?' \
    --strip '(的歌|吧|呀)$' \
    '小爱同学来一首七里香周节伦吧' examples/songs/songs.txt
C:/mycode/570首自听-FLAC/周杰伦 - 七里香.flac
```

- `-x`：从路径取出 artist（可选）和 title / extract artist (optional) and title from the path
- `-k`：按歌名、歌手匹配，也支持"歌手+歌名"任意顺序 / match title and artist, or both in any order
- `--strip`：第一条忽略播放指令，后两条去掉唤醒词、指令词、语气词 / ignore playback commands, remove wake words and filler

`demo.sh` 是这条命令，`test.sh` 跑 79 条回归用例（需要 jq）：
`demo.sh` wraps this command; `test.sh` runs 79 regression cases (requires jq):

```bash
cd examples/songs
./demo.sh '播放周节伦的琴天'    # → 周杰伦 - 晴天.flac
./demo.sh 起风了 -o json        # 多个版本 → ambiguous: true / several versions
./test.sh                       # | **total** | **79/79** |
```

fzq 不包含知识类映射（如"霉霉 = Taylor Swift"），需要时写进数据字段或在调用前替换查询。
fzq has no knowledge mappings (e.g. nicknames); put them in data fields or rewrite the query beforehand.

## 结构 / Layout

```
src/            main.go input.go path.go engine.go text.go output.go
examples/       contacts.yaml products.json app.jsonl cities.txt
examples/songs/ songs.txt demo.sh test.sh cases.json
```
