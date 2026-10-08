# 原地更新与 CoW：页版本和提交边界

状态：部分已整理 · 代码：[leaf.go](../../btree/leaf.go) 的 `leafInsert`、[tree.go](../../btree/tree.go) 的 `put`（内存 CoW 树，尚未接入 Pager）

## 插入一条 KV 要做的两个选择

- **页级**：直接修改旧页，还是构造一张新页（CoW）？
- **页内**：在有序数据中插入时，怎样组织记录与偏移、怎样减少搬移？

两个选择互相独立。本篇讲页级；页内布局的方案对照见 [页内布局对照](../06-storage-comparisons/01-page-layout-alternatives.md)。前置：[页成本与扇出](../03-btree-pages/02-page-io-cost-and-tree-fanout.md)、[节点字节布局](../03-btree-pages/03-node-format-and-record-addressing.md)。

## 原地更新与 CoW

| | 原地更新 | CoW / 路径复制 |
| --- | --- | --- |
| 做法 | 直接改 buffer pool 里的页 | 不碰旧页，产出新页；从根到叶复制整条路径，最后切换根 |
| 崩溃一致性靠什么 | WAL/日志，外加 torn page 防护 | 旧树始终完整，只靠一次原子的根切换（需要满足写入顺序等条件） |
| 读者 | 需要锁或 MVCC | 读者拿住某个旧根，天然看到快照 |
| 写放大 | 小：只弄脏被改的页（另有日志） | 大：每次提交重写约"树高"张页，且多为随机写 |
| 代表 | InnoDB、PostgreSQL、SQLite | LMDB、bbolt、本项目 |

直观对比：原地插入一条 = 弄脏 1 页 + 1 条日志；CoW 插入一条 = 重写从根到叶的整条路径。

**为什么 CoW 不写日志也能崩溃一致。** CoW 从不改活着的页：新页写完之前，旧树一直有效；最后只切换一个根指针，切换前崩溃就用旧树，切换后崩溃就用新树。但这句话隐含了条件：新页必须先于 meta 落盘、meta 本身不能写坏一半、新 meta 落盘前旧树用到的页不能被改——这三个条件见 [发布到磁盘上的条件](../04-btree-tree/04-root-split-and-publication.md#发布到磁盘上的条件)。WAL 的职责也不只是防止撕裂页，两条路线的完整对比见 [与 WAL 的区别](../04-btree-tree/04-root-split-and-publication.md#与-wal-的区别)。

## LMDB 与 bbolt

- **LMDB**：CoW B+Tree（影子分页）的代表。写时复制从根到叶的整条路径，新页写进空闲页，最后原子地切换双 meta 中的一个，让新根生效。无 WAL 也能崩溃一致；读者无锁，拿到的是快照。代价是写放大、旧页占空间、单写者。
- **bbolt**（Bolt 的延续）：同样是 CoW + 双 meta + freelist，单写者多读者。版本与旧页回收见 [MVCC 与版本回收](../06-storage-comparisons/04-mvcc-and-version-reclamation.md)。

产品细节以源码为准；bbolt 源码加入 references 后逐项核对。

## CoW 处处不方便，为什么还选它

写 B+Tree 时遇到的不便——路径复制、页号一层层往上传（[递归 Put](../04-btree-tree/03-recursive-put-and-path-copy.md)）、只能有一个发布点（[根分裂与发布](../04-btree-tree/04-root-split-and-publication.md)）、无法维护叶子链（[扫描](../04-btree-tree/05-scan-and-leaf-links.md)）——都是 CoW 的代价。它的收益大多落在提交、恢复和并发读上：

- **崩溃恢复几乎不需要代码**：没有 WAL、重放和 checkpoint，打开时选一个有效的 meta 即可。
- **读者不阻塞写者**：读事务拿住某个根，看到一致快照；写者只写新页，不动读者正在读的页。游标的快照性质就来自这里。
- **mmap 零拷贝读是安全的**：页写好后在回收之前不再原地修改，才能把指向 mmap 的切片直接交给调用者（见 [value 借用](../04-btree-tree/02-root-and-tree-search.md#value-借用还是复制)）。原地更新的引擎做不到。
- **代码小**：不需要 buffer pool、WAL、undo。

bbolt 的目标是嵌入式、读多写少、简单可靠（etcd、Consul 的 raft-boltdb 都用它），在这些场景里这笔交易划算。

**复制代价按提交计，不按每次 Put 计。** 一个写事务里，节点第一次被改时解码成内存 node，之后在内存里修改，提交时统一写出，同一条路径上的页只写一次。如果每次 put 都是一次独立提交，就要每次复制整条路径；把多次修改合并成一个批次（[原子写批次](06-atomic-write-batches.md)）能把写放大分摊掉。

本项目的取舍：用 CoW，可以不给 B+Tree 写 WAL 与恢复逻辑就得到崩溃一致性；WAL 会在 Raft 磁盘日志与 Rust 阶段的 LSM 中另外实现。

## 大纲

- 页版本、发布与同步之间的具体依赖（未展开，见 [提交顺序与同步](04-commit-order-and-sync.md)）。
- 部分写入、提交结果不确定与恢复选择（未展开）。
- 内存 CoW 树如何接入 Pager（未展开，见 [树与 Pager 接入](02-tree-pager-integration.md)）。
- 写放大、批量更新与故障验证的实际测量（未展开）。

返回 [本组目录](README.md)。
