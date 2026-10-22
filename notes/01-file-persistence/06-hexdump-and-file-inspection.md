# hexdump：逐字节查看文件与 Pager 页

状态：已有正文 · 代码：[cmd/dbdump](../../cmd/dbdump/main.go)

## 做什么

把文件逐字节以十六进制和 ASCII 并排显示：

- `cat` 直接输出内容，不适合用来理解任意二进制字节。
- `hexdump` 展示原始字节，不替你解释数据库格式。

调试数据库页布局、二进制协议和文件格式时，这让“文件就是一串字节”变得可观察。

## 怎么用

```bash
hexdump -C <文件>           # Canonical 两栏格式
hexdump -C <文件> | head    # 只看前几行
hexdump -C <文件> | less    # 大文件分页看

# 只看文件特定字节范围
hexdump -C -s <offset> -n <length> <文件>
# 例：4096 字节页大小下，看 page ID 1 的前 256 字节
hexdump -C -s 4096 -n 256 db.dat
```

本项目习惯总是加 `-C`，便于对照每个字节与右侧文本；不加 `-C` 的默认格式不同，但也能用。

替代工具（具体参数以本机手册为准）：

```bash
xxd <文件>
od -A x -t x1z -v <文件>
```

## 看输出

```text
00000000  58 45 4c 4c 4f 20 78 4d  41 50 21 20 54 68 69 73  |XELLO xMAP! This|
└──┬───┘  └─────────────┬──────────────┘  └──────┬────────┘
偏移量    每个字节的十六进制值                    ASCII 解读
(offset)  16 字节一行，中间空格分隔               不可打印字节显示为 .
```

逐列拆解：

- `00000000`：这一行从文件 offset 0 开始，偏移以十六进制显示。
- `58`：第 0 字节，`0x58 = 88 = ASCII 'X'`。
- `45`：第 1 字节，表示 `'E'`。
- 右边 `|XELLO xMAP! This|`：同样的 16 字节按可显示字符表达。

连续相同的行会折叠为 `*`，避免重复输出：

```text
00000020   ... 65 2e 0a 00 00 00 ...
*
00001000  ...
```

`*` 表示中间省略了相同的完整行，常见的是一段零字节。

## 在数据库开发中观察什么

例如某页设计为“前 2 字节是类型，接下来 2 字节是记录数，后面是 key/value”，可以用 hexdump 对照编码是否写在预期位置。

它补充 `fmt.Println` 看不到的字节级细节。注意 hexdump 看到的是**文件读取的结果**，不一定是磁盘上的字节：普通读可能经过页缓存，所以 hexdump 本身不能证明数据已经持久化。调试插入、分裂、删除时，可以结合 hexdump 观察页内变化，但正确性仍要靠断言与测试。

## 本项目的格式观察入口

[dbdump](../../cmd/dbdump/main.go) 将现有 Pager 格式中的页号、元信息和字节翻译为输出。在仓库根目录运行：

```bash
go run ./cmd/dbdump <文件>
go run ./cmd/dbdump --bytes 64 <文件>
go run ./cmd/dbdump --bytes 256 <文件>
go run ./cmd/dbdump --page 3 <文件>
```

dbdump 解读 Pager 的 meta 与字节，不是完整的格式校验器，能打印出内容不等于格式合法。

## 关联

- [缓存、映射与 Pager](../02-cache-pager/README.md)：逻辑页、页号、偏移与文件长度。
- [节点格式与布局](../03-btree-pages/03-node-format-and-record-addressing.md)：B+Tree 页的字节布局。
- [文件与进程观察工具](07-file-inspection-tools.md)：其余工具。

返回[本组目录](README.md)。
