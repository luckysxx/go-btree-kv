# 存储引擎深入对照

本组把本项目的设计放到更大的设计空间里对照：页内布局、长 key、LSM、MVCC。它们不是首版的完成条件。

| 顺序 | 笔记 | 状态 | 主要内容 |
| --- | --- | --- | --- |
| K1 | [页内布局对照](01-page-layout-alternatives.md) | 部分已整理 | 整页重建、挪指针数组、堆放 + 链表；SQLite、InnoDB、PostgreSQL、LMDB、bbolt |
| K2 | [长 key 与分隔键](02-large-keys-and-separators.md) | 部分已整理 | 短引用、短分隔键、前缀压缩、工业实现 |
| K3 | [B+Tree 与 LSM](03-btree-and-lsm.md) | 部分已整理 | 从追加日志推出 LSM、墓碑、compaction 与分层、WAL 与 MANIFEST、三种放大、业界选型 |
| K4 | [MVCC 与版本回收](04-mvcc-and-version-reclamation.md) | 部分已整理 | CoW 快照与多版本的区别、回收条件 |

产品细节以具体版本的源码和文档为准。页级更新的对照见 [E1 原地更新与 CoW](../05-storage-recovery/01-in-place-update-and-cow.md)。参考源码与论文见 [references](../../../references/README.md)。

返回 [存储笔记总目录](../README.md) · [完整知识点清单](../../../docs/curriculum.md#k)。
