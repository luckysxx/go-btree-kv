# 页内布局对照：连续记录、指针数组与链表

状态：部分已整理（产品细节待按版本和源码核实）· 代码：[leaf.go](../../btree/leaf.go) 的 `leafAppendKV`、`leafInsert`

页内组织与页级更新（[原地更新 / CoW](../05-storage-recovery/01-in-place-update-and-cow.md)）是两个独立的选择。本项目的 offset 是记录区内的相对结束位置，与其他实现的 cell pointer / line pointer 并不等价。

## 往有序页中间插入：挪什么

| 手法 | 插入时挪什么 | 代表 | 代价 |
| --- | --- | --- | --- |
| 整页重建 | 重写整页所有字节 | 本项目、bbolt | 写放大；代码最简单，与 CoW 自然配合 |
| 挪定长指针数组 | 只挪小的定长偏移数组，变长数据本体不动 | SQLite（2 字节 cell 指针）、PostgreSQL（4 字节 line pointer） | 挪 O(B) 个小指针，比挪变长数据省 |
| 堆放 + 有序链表 | 记录按插入顺序堆放，用 next 偏移串成有序链表，只改几个指针 | InnoDB | 不挪数据，但需要稀疏的 page directory 支持二分，并定期整页重组 |

## 产品例子

### SQLite

- **页内**：页头后是按 key 有序的 cell pointer array（2 字节偏移），cell 内容从页尾往前堆。结构上与本项目的 offset 表 + KV 区相近。
- **插入**：二分定位 → 从空闲区或 freeblock 链表分配 cell 空间 → 写 cell 内容（其他 cell 不动）→ 在 pointer array 中插入一个 2 字节项。
- **空间回收**：freeblock 单链表加碎片字节计数，碎片多了整页整理。
- **页级**：原地更新；崩溃一致性靠 rollback journal 或 WAL 模式。

与本项目格式相近，但改法不同：它原地修改、只挪 2 字节指针；本项目重建整页。

### InnoDB

- **页内**：记录**不保持物理有序**，按插入顺序堆放，记录头里的 next-record 偏移把它们串成按 key 有序的单链表。页尾有稀疏的 page directory（每 4～8 条一个 slot）供二分粗定位。
- **插入**：directory 二分 + 链表精确定位 → 从空闲区或 PAGE_FREE 链表取空间写记录 → 改几个 next 指针接上。不挪记录，也不挪指针数组。
- **回收**：删除标记 + PAGE_FREE 链表；碎片靠 page reorganize 压缩。
- **页级**：原地更新；redo log（WAL）+ doublewrite buffer 防 torn page。

### PostgreSQL（nbtree 索引页）

- **页内**：页头后是 line pointer（ItemId）数组，每项 4 字节；tuple 从页尾往前堆。btree 要求 line pointer 按 key 有序，插入时挪这个小数组，tuple 本体不动。
- **页级**：原地更新；WAL + full-page-writes 防 torn page。
- 与 SQLite 思路相同（挪定长小指针），指针宽度不同。

### LMDB

复制出的新页内仍按序插入，会 memmove 页内的指针数组。

### bbolt

修改页时把页解码成内存 node（inode 切片），在切片里有序插入，提交时把整个 node 重新编码进一张新页（spill）。这与本项目的"整页重建"是同一个思路，区别是本项目直接在 `[]byte` 上重建，省掉了 Go 结构体这一层（结构体与字节表示的取舍见 [内存页存储](../04-btree-tree/01-memory-page-store.md#什么时候用结构体和方法)）。

## 大纲

- 连续编码与 slot / cell pointer 布局的字段、偏移和所有权对照（未展开）。
- 空闲空间、删除碎片、整理触发与失败边界（未展开）。
- 选定版本逐函数核对以上产品例子（未展开）。
- 独立实验比较搬移字节、空间利用率与更新成本（未展开）。

当前编码见 [节点格式](../03-btree-pages/03-node-format-and-record-addressing.md)。返回 [本组目录](README.md)。
