# 单机存储

存储这条线的基础与第一个引擎：从文件 API 和 mmap 出发，构建按页访问的 Pager，再实现一个可恢复的 CoW B+Tree 单机 KV（Go）。模块名仍为 `dbstudy`，目录名从 DB-study 调整为 storage。

本目录与 [`lsm/`](../lsm/README.md)（与本目录并列）的分工：

- **这里**放通用的存储知识与第一个引擎：文件与持久化、页缓存与 mmap、提交与恢复、并发，以及各引擎的对照（含 LSM 的概念）。这些笔记不属于某一种引擎，LSM 阶段继续引用，每篇带有"LSM 中的对应"小节。
- **`lsm/`** 放第二个引擎：用 Rust 写的 LSM 及其实现笔记（memtable、SST 格式、compaction 等），需要通用知识时链接回这里。

目录名保持 `storage` 而不是按引擎命名，正是因为其中一半内容是两种引擎共用的。

## 代码地图

| 目录 | 作用 | 读代码的入口 |
| --- | --- | --- |
| pager | 按页读写文件：只读分段映射、WriteAt、Sync、关闭 | [pager.go](pager/pager.go) |
| btree | 页内操作、整树（Get/Put/Delete/游标/检查）、单文件事务与提交 | [函数地图](btree/README.md)、[tree.go](btree/tree.go)、[db.go](btree/db.go)、[tx.go](btree/tx.go) |
| cmd/dbdump | 只读查看文件中的页和字节 | [main.go](cmd/dbdump/main.go) |
| experiments/atomic-save | 三种整文件保存方案 | [main.go](experiments/atomic-save/main.go) |
| experiments/file-api | open、write、fsync、rename 小实验 | [目录](experiments/file-api/) |
| experiments/mmap | 基础映射、remap 陷阱、分段映射、造样本 | [目录](experiments/mmap/) |
| experiments/unix | 文件描述符入门 | [main.go](experiments/unix/main.go) |
| notes | 技术笔记 | [阅读索引](notes/README.md) |

## 运行

在 storage 目录内：

```sh
go test ./...
go test ./pager ./btree
go run ./cmd/dbdump --bytes 64 /path/to/sample.db
```

`dbdump` 的选项放在文件名前；不足一页的输入会报错，不会继续解析。实验每次创建并打印独立临时目录，保留原有运行命令，详见 [实验运行、预期与验证限制](experiments/README.md)。`step2_naive_remap` 与显式 `--bounds-failure` 是故意错误演示，排除普通 smoke。

## 当前边界

查阅文档：[API 参考](../docs/api-reference.md#pager) · [叶子页接口契约](../docs/interface-contracts.md#leaf) · [全部声明](../docs/api-symbols.md)。

Pager 的跨 chunk 重开丢数据问题已修复并增加回归测试，见 [此次改动导读](notes/02-cache-pager/05-chunked-mmap-and-pager-ranges.md)。B+Tree 已有完整的内存树，并经事务接到 Pager：meta 为单份（无校验和），尚无双 meta、页回收或崩溃注入验证。详细证据统一见 [当前状态](../docs/status.md)。

旧路线和旧差距清单在 [docs/archive](docs/archive/README.md)，不作为当前计划。
