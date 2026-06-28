# 笔记 08：插入一条 KV，生产数据库到底怎么改页？（两条正交轴）

> 写 ch03 3.3「叶子 Insert」前的 trade-off 课。我们选了"重建整页"，
> 但这只是一个大设计空间里的一个点。把空间钉清楚，才知道自己选了什么、放弃了什么。
>
> 核心：**"插入"涉及两个正交的决策轴，别混成一团。**
> - 轴 1（页级）：改老页 vs 产新页（CoW）
> - 轴 2（页内）：插有序数据时怎么少挪/不挪

关联：[[06-search-structures-theory]]（摸页贵的成本模型）、[[07-node-format]]（我们的页内格式）、[[production-gaps]] P1.6（WAL）。

---

## 轴 1（页级）：原地 mutate vs CoW / 重建

决定 crash-safe 怎么来、有没有 MVCC、写放大多大。

| | 原地 mutate | CoW / 重建（**我们**） |
|---|---|---|
| 做法 | 直接改 buffer pool 里那页 | 不碰老页，产新页；root→leaf 路径复制，最后原子翻转 meta |
| crash-safe | **必须 WAL/journal** + torn-page 防护 | **天生安全**：老树始终完整，meta 翻转是唯一原子点，从不覆盖活数据 → 无 torn-page |
| MVCC / 读锁 | 要加 latch/锁 | **读无锁**，看老版本快照 |
| 写放大 | 小（只 dirty 改动页） | 大（每写重写 ~树高 个页） |
| 代表 | InnoDB、PostgreSQL、SQLite | **LMDB、BoltDB**、我们 |

**体感**：原地插 1 条 = dirty 1 页 + 1 条 WAL；CoW 插 1 条 = 重写 root→leaf 整条路径（我们 ~4 页）。**写放大 ≈ 树高**。LMDB/Bolt 接受它，换"无 WAL 也安全 + 无锁读"。

> 为什么 CoW 不用 WAL 也安全：WAL 解决的是"原地改到一半崩了，页处于半新半旧的撕裂态"。CoW 从不改活页，新页写完前老树 100% 有效，最后只翻一个 meta 指针（一次原子写），翻之前崩 = 用老树，翻之后崩 = 用新树，**没有中间态**。

---

## 轴 2（页内）：插有序数据，怎么少挪甚至不挪

即便原地改，"往有序区中间插"也要面对挪动。三种手法，从最糙到最精：

| 手法 | 插入时挪什么 | 谁用 | 代价 |
|---|---|---|---|
| **整页重建**（我们 3.3） | 重写整页所有字节 | 我们 / BoltDB | 写放大，但代码最简、与 CoW 天生配 |
| **挪定长指针数组** | 只挪小的定长偏移数组，**变长数据本体不动** | **SQLite**(2B cell 指针)、**PostgreSQL**(4B line pointer) | 挪 O(B) 个小指针，省于挪变长数据 |
| **堆放 + 有序链表** | **啥都不挪**：记录按插入序堆放，用 next 偏移串成有序链表，改几个指针 | **InnoDB** | 不挪数据，但需稀疏 page directory 做二分 + 定期整页重组 |

---

## 逐个生产系统怎么插一条

### SQLite —— 和我们页内格式几乎同源
- **页内**：页头后跟 **cell pointer array**（2B 偏移，按 key 有序）；cell 内容从**页尾往前**堆。↔ 我们的 offset 数组 + KV 区，结构基本一样。
- **插入**：二分定位 → 从 free space 或 **freeblock 链表**分配 cell 空间 → 写 cell 内容（不动其他 cell）→ 在 cell pointer array 里插一个 2B 项（**只挪这个小指针数组**）。
- **空间回收**：freeblock 单链表 + fragmented bytes 计数；碎片多了 defragment 整页。
- **页级**：原地 mutate；crash-safe 靠 rollback journal 或 WAL 模式。
- → **同源格式，不同改法**：它原地改 + 只挪 2B 指针 + freeblock 回收；我们重建整页。

### InnoDB —— 最"插入友好"的页内设计
- **页内**：记录**不保持物理有序**！按插入顺序堆放(heap)，每条记录头里的 next-record 偏移把它们串成**按 key 有序的单链表**。页尾另有 **page directory**（稀疏 slot，每 4~8 条一个）供二分粗定位。
- **插入**：directory 二分 + 链表精定位 → 从 free space 或 **PAGE_FREE 链表**拿空间写记录 → 改几个 next 指针接上链表。**不挪任何记录、也不挪指针数组**。
- **回收**：删除标记 + PAGE_FREE 链表；碎片靠 **page reorganize** 压缩。
- **页级**：原地 mutate；redo log(WAL) + **doublewrite buffer** 防 torn page。

### PostgreSQL（nbtree 索引页）
- **页内**：页头后是 **line pointer (ItemId) 数组**（每项 4B）；tuple 从页尾往前堆。btree 要求 line pointer 按 key 有序 → 插入挪这个小定长指针数组，**tuple 本体不动**。
- **页级**：原地 mutate；WAL + **full-page-writes** 防 torn page。
- → 和 SQLite 同思路（挪定长小指针），区别仅指针宽度（SQLite 2B 偏移 / PG 4B ItemId）。

### LMDB —— CoW B+Tree 的祖师爷（我们这条路）
- **页级**：CoW / 影子分页。写时 root→leaf **复制整条路径**(path-copying)，改副本写到空闲页，最后**原子翻转双 meta** 让新 root 生效。老页等无 reader 引用后回收（MVCC by page version）。
- **页内**：复制出的页里仍有序插入，会 memmove 页内指针数组。
- **好处**：无 WAL 也 crash-safe；读无锁快照。**代价**：写放大（每写复制 O(log n) 页）+ 老页占空间 + 单写者。

### BoltDB / bbolt —— 我们 3.3 做法的真身
- **页级**：同 LMDB（CoW + 双 meta + freelist + MVCC）。
- **关键**：改页时把页**反序列化成内存 node**（inode 切片）→ 切片里有序插入 → commit 时整个 node **重新序列化进一个新页**(spill)。
- → **这几乎就是 3.3 的"重建整页"**。我们只是省掉 Go struct 中转，直接在 `[]byte` 上重建。**我们写的就是 CoW B+Tree 家族的做法。**

### LSM（RocksDB / LevelDB）—— 直接绕开"往有序页插"
- **根本不插有序页**：写 → 内存 **memtable(skiplist)** + WAL append → 满了 flush 成**不可变 SSTable** → 后台 **compaction** 合并。
- **trade**：写快（顺序写、批量），代价是读放大（查多层）+ 写放大/空间放大（compaction）。
- ⚠️ **TiKV 真正的引擎就是 RocksDB(LSM)，不是 B+Tree**。

---

## 落点与对 mini-TiKV 的意义

- 我们的 B+Tree 属于 **SQLite / BoltDB / LMDB 家族**（有序页 + 摸页贵）。
- **TiKV 选了 LSM**（写密集、SSD 顺序写友好）。
- 写这套 B+Tree 的价值：**吃透"有序磁盘存储引擎"的全部 trade-off**——这是读懂 RocksDB（及它为何不这么做）的前提。真启动 mini-TiKV 时，要么复用这套 B+Tree，要么**带着 trade-off 认知主动选 LSM**，这种判断力正是面试想看的。

---

## 一页速记

1. 插入有**两条正交轴**：页级（原地 vs CoW）、页内（少挪手法）。
2. 页级：原地→小写放大但要 WAL；CoW→无 WAL 自安全 + 无锁读，但写放大≈树高。
3. 页内三招：整页重建（我们/Bolt）｜挪定长指针数组（SQLite/PG）｜堆放+链表（InnoDB，啥都不挪）。
4. **我们的格式 ≈ SQLite**，但 SQLite 原地改、我们重建整页（= BoltDB 路线）。
5. **TiKV = RocksDB = LSM**，根本不在这张表里——它绕开了"往有序页插"。
