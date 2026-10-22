# 存储笔记索引

| 组 | 入口 | 内容范围 |
| --- | --- | --- |
| A 文件、可见性与持久化 | [七篇](01-file-persistence/README.md) | 文件 API、数据状态、同步、原子替换、hexdump |
| B 缓存、映射与 Pager | [八篇](02-cache-pager/README.md) | 虚拟内存、页缓存、扩容、三个范围、页生命周期、缓存实验 |
| C B+Tree 页内算法 | [十三篇](03-btree-pages/README.md) | 搜索模型、页格式、叶子与内部页算法 |
| D 完整 B+Tree | [七篇](04-btree-tree/README.md) | 页存储、查找、路径复制、根分裂与发布、游标扫描、删除、全树检查 |
| E 单机提交、恢复与空间管理 | [八篇](05-storage-recovery/README.md) | CoW 与原地更新、提交顺序、恢复、写批次、页回收、单机 API |
| K 存储引擎深入对照 | [四篇](06-storage-comparisons/README.md) | 页内布局、长 key、LSM、MVCC |

每篇标题下有一行状态：已有正文（讲解完整）、部分已整理（有些项只列了要点，标"（未展开）"）、未教学（只有大纲）。

前五组（A～E）按搭建这个引擎的顺序排列，从文件 API 一直讲到可恢复的单机 KV；K 组把它和其他设计（LSM、MVCC 等）对照。文中的"通用笔记《标题》""验证与观测笔记《标题》"指作者另外的笔记，尚未公开，只是延伸阅读，各篇不读它们也能看懂。LSM 的实现在另一个仓库 [rust-lsm](https://github.com/luckysxx/rust-lsm)。

## 代码与资料

- [代码与运行方式](../README.md)、[B+Tree 函数地图](../btree/README.md)、[文件和 mmap 实验](../experiments/README.md)。
- [已知问题与限制](../README.md#已知问题与限制)。
- 对照的源码是 [bbolt v1.5.0](https://github.com/etcd-io/bbolt/tree/v1.5.0)，链接都指向这个版本。
