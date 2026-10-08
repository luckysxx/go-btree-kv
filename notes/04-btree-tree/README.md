# 从页算法到完整 B+Tree

本组把页内算法（C 组）组装成一棵完整的内存 CoW B+Tree，顺序是"页存取 → 查找 → 更新路径 → 根 → 扫描 → 删除 → 全树验证"。完整范围见 [D 组清单](../../../docs/curriculum.md#d)。

| 笔记 | 状态 | 主要内容 |
| --- | --- | --- |
| [01 内存页存储](01-memory-page-store.md) | 已有正文 | 页号分配、保存时复制与读取时借用、错误形状、结构体使用标准、bbolt 对照 |
| [02 根定位与整树查找](02-root-and-tree-search.md) | 已有正文 | 树对象的职责、空叶子根、递归下降与防环、value 借用有效期 |
| [03 递归 Put 与路径复制](03-recursive-put-and-path-copy.md) | 已有正文 | 向父层交回目录项、页号与下标、路径复制、minKey 更新、失败与发布规则 |
| [04 根分裂与发布](04-root-split-and-publication.md) | 已有正文 | 新根构建、单一发布点、磁盘发布的三个条件、与 WAL 的区别 |
| [05 扫描与叶子链接](05-scan-and-leaf-links.md) | 已有正文 | 叶子链与 CoW 不相容、游标路径栈、快照扫描 |
| [06 删除、再平衡与根收缩](06-delete-rebalance-and-root-shrink.md) | 部分已整理 | 三种删除结果、摘除空页、根收缩；合并与借位未展开 |
| [07 全树不变量与模型测试](07-tree-invariants-and-model-tests.md) | 部分已整理 | 全树检查、参考模型对照；页字节级校验未展开 |

代码入口：[B+Tree 函数地图](../../btree/README.md)。本组实现的是内存中的树，未接入 Pager；落盘、提交与恢复见 [E 组](../05-storage-recovery/README.md)。实现与验证状态见 [当前状态](../../../docs/status.md)。

前置：[B+Tree 页内算法组](../03-btree-pages/README.md)。返回 [存储笔记总目录](../README.md)。
