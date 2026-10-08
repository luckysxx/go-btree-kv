# OS 页缓存与显式 buffer pool

状态：部分已整理（本项目主 Pager 使用 mmap，没有独立的 buffer pool）

## 为什么从 mmap 开始

用 mmap 起步有三个方便之处：通过 slice 直接访问文件页；减少显式用户缓冲区的拷贝；哪些文件页驻留内存交给操作系统管理。上层因此可以先集中精力在页格式与树算法上。

相应的代价：缺页不发生在显式的 `ReadAt` 调用点；映射访问可能以信号形式失败；扩容与旧页视图的生命周期要另外管理（见 [虚拟内存与缺页](02-virtual-memory-and-page-faults.md)、[页缓存与 mmap](03-page-cache-and-mmap.md)、[页生命周期](06-page-lifetime-and-pager-errors.md)）。更重要的是写回顺序不受控制，这会影响提交协议（见 [磁盘发布的条件](../04-btree-tree/04-root-split-and-publication.md#发布到磁盘上的条件)）。

"省去自己写 buffer pool"不代表操作系统替引擎实现了提交、并发控制或内存预算。

## 产品对照

常见的归类是：SQLite（部分模式）、LMDB、bbolt 用 mmap；PostgreSQL、MySQL InnoDB 用显式 buffer pool。具体默认配置与实现要按版本和源码核实，不宜当作排他的分类。

## 大纲

- OS 页缓存与引擎缓存的职责边界（未展开）。
- 显式缓存的装载、pin/unpin、淘汰与脏页处理（未展开）。
- 内存预算、扫描污染、预读和 I/O 错误处理的对比（未展开）。
- 两层缓存（引擎缓存 + OS 页缓存）与资源测量（未展开）。
- 开源实现按版本对照（未展开）。

相关实验：[缓存替换小实验](08-cache-replacement-experiment.md)。返回 [本组目录](README.md)。
