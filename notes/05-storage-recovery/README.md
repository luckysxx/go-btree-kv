# 单机提交、恢复与空间管理

本组把内存中的 CoW B+Tree 接到 Pager 上：提交顺序、打开与恢复、写批次、页回收，最后形成单机 KV API。

| 编号 | 笔记 | 状态 | 主要范围 |
| --- | --- | --- | --- |
| E1 | [原地更新与 CoW](01-in-place-update-and-cow.md) | 部分已整理 | 两种页级更新、LMDB 与 bbolt、CoW 的代价与收益 |
| E2 | [树与 Pager 接入](02-tree-pager-integration.md) | 部分已整理 | 页存取边界、磁盘分配、根元信息 |
| E3 | [文件格式与元数据校验](03-file-format-and-meta-validation.md) | 部分已整理 | 元信息入口、校验和、双 meta 与 txid 槽位不变量、LSM 对照 |
| E4 | [提交顺序与同步](04-commit-order-and-sync.md) | 部分已整理 | mmap 写入顺序、两次 fsync、fsync 失败不能重试、WAL 一次 fsync 的原因、LSM 对照 |
| E5 | [打开与故障恢复](05-open-and-crash-recovery.md) | 部分已整理 | 恢复选择、残留页、截短与损坏 |
| E6 | [原子写批次](06-atomic-write-batches.md) | 部分已整理 | 多项更新、提交结果与失败语义 |
| E7 | [页回收与读者](07-page-reclamation-and-readers.md) | 部分已整理 | 两类可回收的页、重用的两个条件、空闲清单与待回收表、重启时重算、账目检查、LSM 对照 |
| E8 | [单机 KV API](08-single-node-kv-api.md) | 部分已整理 | 单写者、读写与关闭、导出边界 |

磁盘发布的三个条件已在 [根分裂与发布](../04-btree-tree/04-root-split-and-publication.md#发布到磁盘上的条件) 中预先讲过，E4、E5 会在此基础上展开。代码入口：[pager.go](../../pager/pager.go)、[B+Tree 函数地图](../../btree/README.md)。

返回 [存储笔记总目录](../README.md)。
