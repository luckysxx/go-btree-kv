# B+Tree 与 LSM：有序页和不可变文件的取舍

状态：部分已整理 · LSM 的实现在 [lsm/](../../../lsm/README.md)

## LSM 的基本思路

LSM 是 Log-Structured Merge-tree 的缩写，出自 1996 年 O'Neil 等人的同名论文。Log-Structured 指数据只以"新写一段"的方式落盘，不回头修改已经写下的内容；Merge 指整理数据的手段是把几个有序文件归并成新的有序文件；Tree 来自论文中由小到大的一串树形组件，现在的实现是分层的文件，名字沿用下来。

B+Tree 的写入是"往有序页里插入"。LSM（RocksDB、LevelDB）直接绕开这件事：

- 写入先追加到 WAL，并放进内存中的 **memtable**（常见实现是跳表）。
- memtable 满了就 flush 成一个**不可变**的有序文件 **SSTable**。
- 后台 **compaction** 把多个 SSTable 合并成更少、更大的文件，顺带清理被覆盖和被删除的数据。

取舍：写入是顺序、批量的，适合写多负载；代价是**读放大**（一个 key 可能要查多层文件，靠 Bloom filter 和缓存弥补），以及 compaction 带来的**写放大和空间放大**。

## 从只追加日志推出 LSM

**起点：只追加 + 版本号。** 每次写入追加一条带递增序号（RocksDB 叫 sequence number）的记录，同一 key 以序号最大者为准。问题是 get 只能从尾部往前扫，数据越多越慢。

**改进一：内存哈希索引**（Bitcask 的做法）。内存里维护 key → 日志偏移，get 一次磁盘读。代价：所有 key 必须放进内存，且哈希不支持范围扫描。

**改进二：让数据有序**（LSM 的做法）。写入先进内存中的有序结构 **memtable**（常用跳表）；写满后整体顺序写出为有序、不可变的 **SST** 文件。SST 内部有序，只需稀疏索引（如每个数据块的首 key）即可二分定位，也支持范围扫描。

读路径从新到旧，命中即停：

```
get(k)：memtable → 较新的 SST → 较旧的 SST → …
```

新旧由两层决定：文件之间按生成先后，文件之内按序号。

## 删除：墓碑

旧值在不可变的 SST 里删不掉，删除只能写一条新记录"k 已删除"，即 **tombstone**。读到墓碑就返回不存在，不再看更旧的值。

**常见错误：提前丢弃墓碑。** 只要更旧的文件里还有 k，丢掉墓碑就会让旧值复活。只有确认下面没有更旧的数据（例如合并到最底层）时，墓碑才能丢弃。

## compaction 与分层

SST 越积越多：get 要查的文件变多（读放大），被覆盖的旧值与墓碑占着空间（空间放大）。**compaction** 把若干 SST 多路归并，每个 key 只留最新版本、丢弃可丢的墓碑，写出新 SST 后删除旧文件。思路与 Raft 日志压缩同类（整理掉不再需要的历史），做法不同：Raft 是快照状态机后丢日志前缀，LSM 是归并有序文件。

常见的分层（leveled）组织：

```
memtable ──flush──→ L0：刚刷下的文件，彼此 key 范围可重叠
                     ↓ compaction
                    L1：层内 key 范围不重叠，总量约为上一层的 10 倍
                     ↓
                    … Ln：最底层，墓碑在此才能安全丢弃
```

代价是**写放大**：一条数据从 L0 逐层合并到 Ln，会被反复读出、重写。

SST 不可变，写完不再修改，天然适合放在对象存储上（例如 SlateDB 直接建在对象存储上）；原地修改页的 B+Tree 难以这样部署。WAL 与 MANIFEST 在对象存储上要另行处理，见通用笔记 [[_knowledge/数据库/07-文件系统、对象存储与存储引擎|文件系统、对象存储与存储引擎]]。

## 崩溃与 WAL

memtable 在内存中，进程崩溃即丢失。写入先追加到 **WAL** 并按策略 fsync，再插入 memtable，此时才算成功；重启时重放 WAL 重建 memtable。memtable 刷成 SST 且被元数据记录后，对应的 WAL 才能删除。

"当前有哪些 SST"记录在只追加的元数据日志 **MANIFEST** 中，入口是 **CURRENT** 文件（写临时文件 → fsync → rename 原子切换）。二者合起来承担 B+Tree 中 meta 页的角色。

```
写：put → 追加 WAL（fsync）→ 插入 memtable → 返回成功
     memtable 满 → 写 SST → fsync → 记入 MANIFEST → 删除已刷盘的 WAL
读：get → memtable → L0（新到旧）→ L1 → … → 碰到值或墓碑即停
后台：compaction → 新 SST fsync → 记入 MANIFEST → 旧文件在无读者引用后删除
```

与 Raft 对照：Raft 的 currentTerm、votedFor、log[] 是持久状态，必须在回复 RPC 前写入稳定存储；commitIndex、lastApplied 才是可由持久状态重新推出的易失状态。LSM 的 memtable 属于后者的地位，它的持久性完全来自 WAL。

## 三种放大

| 放大 | 含义 | 来源 | 常见缓解 |
| --- | --- | --- | --- |
| 读放大 | 一次读实际读了多少数据 | 一个 key 可能要查多层文件 | 每个 SST 的 Bloom filter、block cache |
| 写放大 | 写入磁盘的字节 / 用户写入的字节 | compaction 逐层重写 | 调整层级倍数、tiered compaction |
| 空间放大 | 占用空间 / 有效数据量 | 旧版本与墓碑等待 compaction | 更积极的 compaction（与写放大互相牵制） |

三者互相牵制：compaction 越积极，读和空间放大越小、写放大越大。

## 与单机 B+Tree 的对照

| | 本项目的 CoW B+Tree | LSM |
| --- | --- | --- |
| 修改 | 立即把有序结构改好（路径复制新页） | 先顺序追加，整理推迟到后台 |
| 读 | 从根往下一条路径 | 多个来源，从新到旧 |
| 发布 | 写 meta 页 | 追加 MANIFEST 记录 |
| 旧数据回收 | 旧页在无读者后回收（空闲页） | 旧 SST 在无读者引用后删除 |
| 主要代价 | 随机写、路径复制 | 读放大、写放大、空间放大 |

E 组各主题在 LSM 中的对应，写在各篇的"LSM 中的对应"小节。

## B+Tree 也可以把写入攒在内存里

把修改先记在内存、过一阵统一落盘，并不是 LSM 独有的。InnoDB、PostgreSQL 把页改在内存里成为脏页，之后再刷盘，崩溃时靠 WAL 恢复；本项目的事务也是先改内存节点、提交时一次编码。

把这个想法补全：内存里用一张表记下修改；读的时候要先查这张表再查磁盘，否则读不到刚写的数据；要支持范围扫描，这张表得有序；进程崩溃它就没了，所以每次修改还要先追加日志。做到这里得到的就是 memtable 加 WAL，两条路线的前半段相同。区别只在攒够之后怎样落盘：

| | 并回原来的树（B+Tree 加缓冲池加 WAL） | 原样写成一个新的有序文件（LSM） |
| --- | --- | --- |
| 一个 key 的位置 | 只有一个，由 key 决定它在哪一页 | 可以同时存在于多个文件，新的盖住旧的 |
| 落盘时动哪里 | 每个被修改的 key 所在的页，先读出、改一条、整页写回 | 不动任何已有文件 |
| 写的形态 | 位置分散 | 顺序写一个文件 |
| 归位的工作 | 落盘时就做完 | 推迟给 compaction，有序文件之间归并 |

举例：库里 100 GB、4 KB 一页，约 2500 万个叶子页。内存里攒了 100 万条随机 key 的修改，每条约 116 字节。并回树里，这些 key 落在约 98 万个不同的叶子页上，要写出约 4 GB；写成一个新文件只有约 116 MB。

攒一阵再刷，对 B+Tree 省下的是：同一个 key 多次修改只写最后一次、碰巧落在同一页的修改合并、同步次数减少。省不掉的是"每个被碰到的页都要整页重写"。因此它在热点数据能放进内存、或者 key 顺序写入（修改集中在少数页）时效果好；数据远大于内存、写入的 key 又分散时，LSM 的做法便宜得多。

## 从两层到多层

"写入先进 LSM，过一段时间并成 B+Tree"正是论文中最简单的形态：内存里的 C0 接收写入，磁盘上的 C1 是一棵节点全部装满、按顺序存放的类 B 树，后台不断把 C0 归并进 C1。

它的问题是上一节那笔账只是被推后了。归并的代价取决于新数据相对于被并入一方有多小：116 MB 并进 100 GB，相差近千倍，每重写一页，里面几乎全是没有变化的旧数据。

解法是中间垫几层，逐级变大，每次只把一层并进比它大约十倍的下一层：

```
内存 100 MB → 第 1 层 1 GB → 第 2 层 10 GB → 第 3 层 100 GB
```

这样每次归并重写的数据里，新数据约占十分之一。一条记录一路下沉要被重写几遍，但每一遍都是顺序读写。这是 leveled compaction 的来历。

分层之后，最底层的文件彼此 key 范围不重叠、整体有序，每个文件内部有索引块指向数据块；每层是上一层的十倍，约九成的数据都在最底层。把文件的首末 key 与索引块看成内部节点、数据块看成叶子，最底层就是一棵叶子全部装满的 B+Tree，只是更新它的方式是重写文件而不是原地改页。

## 混合的设计与 RUM 猜想

介于两者之间的设计：

- **Bε-tree**（TokuDB 的 fractal tree）：结构是 B 树，每个内部节点带一块缓冲区，写入先进根的缓冲区，满了成批推向下一层。
- **InnoDB 的 change buffer**：二级索引的修改先记在缓冲结构里，等对应的页被读进内存时再合并。
- **WiredTiger 的 LSM 模式**：每一段本身是一个 B 树文件。

RUM 猜想概括了这类取舍：读的开销（Read）、写的开销（Update）、空间的开销（Memory）三者最多优化两个，第三个要付出代价。它是经验规律而不是定理。混合方案同样逃不出去：把数据归位的那一步，原地改页付的是随机写，重写文件付的是写放大；数据归位之前，读要多查几个地方。各种设计的区别在于这笔代价摊在哪里、何时支付。

## B+Tree 与 LSM 的落点

- 本项目的 B+Tree 属于 SQLite / bbolt / LMDB 这一类：有序页，读路径短，延迟可预测。
- TiKV 的存储引擎是 RocksDB（LSM），适合写密集和 SSD 顺序写。
- 先把"有序磁盘存储引擎"的取舍弄透，再去读 RocksDB，才能看懂它为什么不这样做；到了分布式阶段，也能带着这些认识主动选择引擎。

## 业界选型：是不是都是 LSM + WAL

不是清一色。**WAL 几乎人人都用**（B+Tree 引擎也用）；**LSM 在"新兴 + 分布式 + 写多"一类系统里占多数**，大型数据库仍多为 B+Tree。以下归类依据一般了解，具体以各产品版本为准：

| 路线 | 代表 |
| --- | --- |
| B+Tree + WAL | PostgreSQL、MySQL InnoDB、Oracle、SQL Server、SQLite；MongoDB 默认的 WiredTiger 是 B-tree |
| LSM + WAL | RocksDB 系（TiKV、YugabyteDB、MyRocks）、CockroachDB（Pebble）、Cassandra、ScyllaDB、HBase |
| B+Tree + CoW，无 WAL | bbolt、LMDB（本项目当前路线） |
| 其他 | ClickHouse MergeTree（后台合并数据块，面向列存分析）、DuckDB 等列存分析引擎、FoundationDB 的 B+Tree 变体 Redwood |

**新兴分布式系统偏爱 LSM 的原因：**

1. **有现成的成熟引擎。** RocksDB / Pebble 可嵌入、调优手段多，自研同等质量的 B+Tree 引擎成本很高。这更多是工程选择。
2. **写入友好。** 追加日志 + memtable + 整批顺序落盘适合写多负载，对 SSD 友好，也方便压缩。
3. **持久性已经由 Raft 日志承担。** 引擎主要需要写吞吐，这正是 LSM 的长处。

**B+Tree 的优势场景：** 读路径短、延迟可预测，读多或要求稳定延迟的 OLTP 仍偏向它。学术界的 LeanStore、Umbra 等在研究 B-tree 加自管缓冲区（mmap 的问题见 [页缓存与 buffer pool](../02-cache-pager/07-page-cache-versus-buffer-pool.md)）。

**两层日志问题：** Raft 日志本身就是 WAL。如果把 Raft 日志也写进 RocksDB，而 RocksDB 自己也有 WAL，同一份数据就写了两遍日志。TiKV 早期就是这样，后来单独做了 raft-engine 存 Raft 日志。

省掉一层的思路：引擎不单独记 WAL，崩溃后从已 apply 的位置重放 Raft 日志。前提是引擎写入数据时能把 applied index **原子地**一起发布，否则会重复 apply 或漏掉——与 B+Tree 中根页号和高水位由同一次 meta 写入发布同理（见 Raft 的 KV/applied 应用协议，路线图批次 6）。etcd 是另一种分层：Raft 日志存在 etcd 自己的 WAL 中，状态机写入 bbolt，bbolt 本身没有 WAL（CoW + meta 原子发布）。

数据库 WAL 的工作方式、它与 Raft 日志的逐项对照，见通用笔记 [[_knowledge/数据库/05-预写日志与复制日志|预写日志与复制日志]]。B+Tree 与 LSM 之外的结构（消息队列的追加日志、时序与列式存储）见 [[_knowledge/数据库/06-各类数据系统的存储结构|各类数据系统的存储结构]]。WAL 与 CoW 两种崩溃一致性路线的对比见 [与 WAL 的区别](../04-btree-tree/04-root-split-and-publication.md#与-wal-的区别)。

## 大纲

以下各项在 [LSM 引擎的实现笔记](../../../lsm/notes/README.md) 中展开，本篇不再重复：

- memtable 的并发结构（跳表）与 immutable memtable 切换。
- SST 内部格式：数据块、索引块、Bloom filter、footer。
- leveled 与 tiered compaction 的策略选择、compaction 调度与限速。
- 三种放大怎样取数：[基准方案与三种放大的测量](../../../lsm/notes/01-benchmark-plan-and-amplification.md)。
- 对照具体版本的开源源码（mini-lsm、Pebble、RocksDB）。

返回 [本组目录](README.md)。
