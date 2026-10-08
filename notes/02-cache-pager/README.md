# 缓存、映射与 Pager

这一组从文件页号出发，讲地址空间、页缓存与扩容，再回到 Pager 的三个范围与页生命周期。主引擎使用分段 mmap；显式缓存只作为独立实验。

| 顺序 | 笔记 | 状态 | 主要内容 | 代码入口 |
| --- | --- | --- | --- | --- |
| 1 | [文件页与页号](01-file-pages-and-page-ids.md) | 已有正文 | 文件格式、逻辑页、页号到偏移、meta 入口 | `ReadPage`、`metaSlotFor` |
| 2 | [虚拟内存与缺页](02-virtual-memory-and-page-faults.md) | 已有正文 | 虚拟地址、MMU 与页表、按需缺页 | mmap 基础实验 |
| 3 | [页缓存与 mmap](03-page-cache-and-mmap.md) | 已有正文 | 两种访问方式、SHARED/PRIVATE、脏页与写回 | mmap 基础实验、`WritePage`、`Sync` |
| 4 | [文件扩容与稀疏文件](04-file-growth-and-sparse-files.md) | 已有正文 | Truncate 语义、逻辑长度与占用、平台差异 | `growMmap` |
| 5 | [分段映射与三个范围](05-chunked-mmap-and-pager-ranges.md) | 已有正文 | 追加 chunk、fileSize/映射/已分配、重开误截短 | `Open`、`Grow`、`mapUpTo` |
| 6 | [页生命周期与 Pager 错误](06-page-lifetime-and-pager-errors.md) | 部分已整理 | 旧 slice 失效、三种边界、Rust 借用的范围 | `ReadPage`、`Close` |
| 7 | [页缓存与 buffer pool](07-page-cache-versus-buffer-pool.md) | 部分已整理 | mmap 起步的利弊、产品对照 | — |
| 8 | [缓存替换小实验](08-cache-replacement-experiment.md) | 未教学 | LRU、Clock、扫描负载 | 待准备 |

## 代码与实验入口

| 入口 | 用途 | 边界 |
| --- | --- | --- |
| [pager.go](../../pager/pager.go) | 文件分页、分段映射、分配、同步与关闭 | 没有完整的文件校验、失败恢复或提交协议 |
| [pager_test.go](../../pager/pager_test.go) | 分配、读回与正常重开回归 | 正常关闭重开不是崩溃或断电验证 |
| [step1_mmap_basic](../../experiments/mmap/step1_mmap_basic/main.go) | 共享映射与文件可见性 | `--sync` 显式同步；`--bounds-failure` 演示 Go 越界 |
| [step2_naive_remap](../../experiments/mmap/step2_naive_remap/main.go) | 整体重映射后使用旧 slice 的失败实验 | 不保证每次崩溃；不属于正常流程 |
| [step3_chunked_mmap](../../experiments/mmap/step3_chunked_mmap/main.go) | 追加 chunk 后继续访问旧页 | 只演示新文件扩容 |

在 `storage` 目录运行，例如 `go run ./experiments/mmap/step1_mmap_basic`；每次运行使用独立临时目录，参数与清理方式见 [实验入口](../../experiments/README.md)。

上一组：[文件、可见性与持久化](../01-file-persistence/README.md)。下一组：[B+Tree 页内算法](../03-btree-pages/README.md)。返回 [存储笔记总目录](../README.md)。
