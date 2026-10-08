# 分段 mmap 与 Pager 的三个范围

状态：已有正文 · 代码：[pager.go](../../pager/pager.go) 的 `Open`、`Grow`、`mapUpTo`、`ReadPage`、`WritePage`

## 文件变大不会自动扩大已有 slice

```go
data, _ := syscall.Mmap(fd, 0, 8192, /* 其余参数略 */)
fp.Truncate(16384)
data[10000] // slice 长度仍是 8192，Go 下标越界
```

文件长大后，要让新范围可以访问，有两种做法：整体解除映射再重新映射全文件，或者追加新的映射。前者会让已经借出去的页 slice 失效（见 [页生命周期](06-page-lifetime-and-pager-errors.md#整体重映射为什么会让旧-slice-失效)），本项目选择后者。

## 分段映射：追加新 chunk，保留旧 chunk

把文件按固定的 chunk 大小分别映射，文件增长时只追加映射，原有 chunk 不动：

```text
文件偏移： [ chunk 0 ][ chunk 1 ][ chunk 2 ] ……
扩容前：   chunks = [chunk0]
扩容后：   chunks = [chunk0, chunk1]      ← 原来的映射仍在
```

当前 `ChunkSize` 是 16 KiB 的演示值（`PageSize` 是 4 KiB），生产系统的 chunk 大小（比如 64 MiB）要按映射数量与地址空间来选，这里没有测量依据。

```go
byteOff  := int64(N) * PageSize
chunkIdx := byteOff / ChunkSize
inChunk  := byteOff % ChunkSize
return chunks[chunkIdx][inChunk : inChunk+PageSize]
```

chunk 在**文件偏移上连续**，但不要求在**虚拟地址上相邻**：每个 chunk 独立定位。在 macOS 上曾观察到前几个 chunk 地址相邻、之后某个 chunk 跳开很远，原因可能是 Go runtime 等占用了中间地址，未核实。

分段映射让旧页引用在追加 chunk 时保持有效，但**不是永久有效**：`Close` 解除映射、外部进程截短文件等情况仍会使引用失效。

## 三个必须分开的量

| 量 | 代表什么 | 如何得到 |
| --- | --- | --- |
| `fileSize` | 文件的逻辑长度（不是物理预分配量） | `Open` 时 `Stat`；成功扩长后更新 |
| `len(chunks) × ChunkSize` | 本进程已建立映射覆盖的文件范围 | 每次 `mmap` 成功后追加 |
| 高水位 × `PageSize` | 数据库逻辑上已分配的范围（含 meta 页） | 由上层 meta 记录（[btree/db.go](../../btree/db.go)）；打开时读 meta，提交时推进。Pager 本身不记录 |

例如文件长 64 KiB（一次失败提交在高水位之上写过页）、映射已覆盖全部 64 KiB、已提交的高水位为 3 页：三个量分别是 64 KiB、64 KiB、12 KiB，并不矛盾。

不变量：已分配范围不超过文件长度（打开时检查），读取只在文件长度之内访问映射（`ReadPage` 检查页号）。前提是只有一个 Pager 管理这个文件，没有外部进程同时改文件长度（多进程保护尚未实现）。

"文件已经够大"和"本进程已能访问这段文件"是两个条件：文件 64 KiB、已映射 16 KiB、需要访问前 24 KiB 时，只需要**增加映射**；按 16 KiB 粒度追加一段后，映射范围变为 32 KiB，文件仍是 64 KiB。

## 失败例：重开时误截短

如果新建的 Pager 对象里 `fileSize` 初始为 0，第一次扩容到 16 KiB 会把文件 `Truncate` 到 16 KiB，即使真实文件更大也会丢掉尾部。之后按已分配页数扩回去，只能得到零字节，不能恢复旧内容（`Truncate` 的语义见 [文件扩容](04-file-growth-and-sparse-files.md)）。

只从 `Stat` 初始化 `fileSize` 还不够：如果之后因为"文件已经够大"就提前返回，映射也被跳过，`chunks` 仍为空。**不能让一个条件同时代表"文件够长"和"映射已建立"。**

正确的扩容（`Grow` 与 `mapUpTo`）：

1. `Open` 从 `Stat` 记录真实文件长度，只补映射，不改文件长度。
2. 写入时把需求长度向上对齐到 chunk 大小。
3. 只有目标长度大于 `fileSize` 时，才 `Truncate` 扩长并更新 `fileSize`。
4. **独立**检查 `chunks`，补齐缺的映射——文件已经够长也要做这一步。
5. 映射的最后一个 chunk 可能越过文件末尾，越界部分访问会触发 `SIGBUS`，所以读取按文件长度检查页号。

```text
target = 向上对齐后的需求长度
如果文件不够长：扩长文件
如果映射不够长：逐段建立映射
```

## 测试

[pager_test.go](../../pager/pager_test.go)：

- `TestReopenAcrossChunksPreservesPages`：写十页，每页写满标记，`Sync → Close → Open` 后检查所有内容，再追加四页重复检查。
- `TestOpenPreservesFileLength`：文件长度是或不是 chunk 倍数时，`Open` 都保持长度与尾部；之后写入仍能访问。
- `TestReadPageOutOfRange`：越过文件末尾的页号返回错误，而不是访问映射。

这两个测试属于同进程中的正常同步、关闭与重开，不是进程崩溃、断电、多进程写入或故障注入验证。[分段映射实验](../../experiments/mmap/step3_chunked_mmap/main.go) 只演示新文件扩容，不涉及重开。

下一篇：[页生命周期与错误](06-page-lifetime-and-pager-errors.md) · [本组目录](README.md)。
