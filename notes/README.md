# 存储笔记索引

| 组 | 入口 | 内容范围 |
| --- | --- | --- |
| A 文件、可见性与持久化 | [七篇](01-file-persistence/README.md) | 文件 API、数据状态、同步、原子替换、hexdump |
| B 缓存、映射与 Pager | [八篇](02-cache-pager/README.md) | 虚拟内存、页缓存、扩容、三个范围、页生命周期、缓存实验 |
| C B+Tree 页内算法 | [十三篇](03-btree-pages/README.md) | 搜索模型、页格式、叶子与内部页算法 |
| D 完整 B+Tree | [七篇](04-btree-tree/README.md) | 页存储、查找、路径复制、根分裂与发布、游标扫描、删除、全树检查 |
| E 单机提交、恢复与空间管理 | [八篇](05-storage-recovery/README.md) | CoW 与原地更新、提交顺序、恢复、写批次、页回收、单机 API |
| K 存储引擎深入对照 | [四篇](06-storage-comparisons/README.md) | 页内布局、长 key、LSM、MVCC |

每篇标题下有一行状态：已有正文 / 部分已整理 / 未教学。部分已整理的篇在大纲中把未展开的项标"（未展开）"。

## 代码、进度与资料

- [代码与实验运行](../README.md)、[B+Tree 函数地图](../btree/README.md)、[文件和 mmap 实验](../experiments/README.md)。
- [实现与验证状态](../../docs/status.md)、[学习进度](../../docs/learning-progress.md)、[完整清单](../../docs/curriculum.md)。
- [项目总目录](../../docs/index.md)、[学习路线](../../docs/roadmap.md)、[快速回忆](../../docs/refresh.md)。
- [参考源码](../../references/README.md)。
