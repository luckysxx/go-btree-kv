# 树与 Pager 接入

状态：部分已整理 · 代码：[db.go](../../btree/db.go)（meta、打开与初始化、update/view）、[tx.go](../../btree/tx.go)（事务页存储、提交）、[pagestore.go](../../btree/pagestore.go) 的 `pageStore` 接口、[pager.go](../../pager/pager.go)

## 问题

内存 CoW 树只依赖两个页操作：`save` 复制并分配新页号，`get` 借用读取；发布是给 `t.root` 赋值。接到磁盘文件上，要回答：新页什么时候写盘、页号怎么分配、根页号存在哪里、谁负责提交。

## 事务页存储：提交时统一写

一次提交要两次 fsync（新页 → fsync → meta → fsync，见 [提交顺序与同步](04-commit-order-and-sync.md)）。若每次 `save` 都立即写盘并 fsync，一次 put 就要为路径上每一页付一次同步代价。做法是在树与文件之间加一个小事务层，事务期间树使用事务自己的页存储：

```
tx.save(page)  → 分配页号，放入 tx.dirty[页号]，不写盘
tx.get(id)     → 先查 tx.dirty，没有再从只读 mmap 读
tx.commit()    → WriteAt 全部 dirty 页 → fsync → 写 meta → fsync
tx.rollback()  → 丢弃 dirty，不写任何东西
```

`get` 先查 dirty 是必需的：同一操作里会读取刚保存的页（例如根收缩时读新根）。树因此要依赖接口 `pageStore{get, save}`，内存页存储留给单元测试，事务页存储用于磁盘。

语义随之改变：树内给根赋值只表示"事务内的根变了"；**发布是写 meta**，发生在 `tx.commit` 中。单次 put 等于只含一个操作的事务；多个操作放进同一事务就是 [原子写批次](06-atomic-write-batches.md)，路径上的页也只写一次。

## 高水位与页号分配

**高水位**（high-water mark）是已分配页号的上界：以下的页都分配过，以上的页从未使用。没有空闲页时，分配就是取当前高水位作页号并把高水位加一；有空闲页时优先重用（见 [页回收与读者](07-page-reclamation-and-readers.md)）。高水位只随 meta 一起持久化。

事务开始时复制一份 meta，分配只推进这份副本的高水位；提交时把副本写成新 meta，回滚时丢弃副本，高水位自动回到已提交的值。

## 重启只相信 meta

`Open` 读出 meta，得到根页号与高水位，此后认定：根可达的页就是数据库，高水位以上的页不存在。文件实际长度、高水位以上的内容一概不作依据。

| 情形 | 重启后 |
| --- | --- |
| 新页写了一部分或全部，meta 未写就崩溃 | meta 仍是旧根与旧高水位 → 完整旧树；已写的新页在高水位以上，视为不存在，下次分配直接覆盖 |
| 写 meta 之前 WriteAt 或 fsync 报错 | 等于回滚：丢弃 dirty 与事务内高水位，内存状态回到已提交版本 |
| 写 meta 或第二次 fsync 报错 | 磁盘上的 meta 可能新也可能旧，内存无法判断：拒绝后续写事务，关闭重开后由 meta 决定 |
| 第二次 fsync 完成 | 新 meta 生效 → 完整新树 |

因此失败事务分配出的页号无需任何处理即可重用，前提是高水位与根页号由同一次 meta 写入发布。文件长度与数据库边界是两回事，混用会导致把有效数据当作多余而截掉（见 [Pager 文件与映射范围](../02-cache-pager/05-chunked-mmap-and-pager-ranges.md)）。

## 内存中的 meta 与磁盘上的 meta

同一份"当前版本"有两个副本：

| 副本 | 谁读它 | 作用 |
| --- | --- | --- |
| 磁盘页 0 的 meta | 下一次 `Open` | 跨重启的权威版本 |
| 内存中的 `db.meta` | 本进程每次 `begin` | 运行期间新事务从哪个版本出发 |

`Open` 只在打开时从磁盘读一次 meta，之后事务都复制内存中的那份，不再回读磁盘。所以提交写完磁盘 meta 后，还要把内存那份更新（`db.meta = t.meta`），这是内存中的发布点，相当于内存树里给根赋值。

**漏掉它不只是"看不到新数据"。** 下一个事务会从旧根与**旧高水位**出发，重新分配刚提交的页号并覆盖它们——磁盘上的 meta 已经指向这些页，已提交的树就被写坏了。

**顺序：先持久，再可见。** 内存发布必须在第二次 sync 成功之后。提前发布时，若第二次 sync 失败，内存已是新版本而磁盘不一定是；后续事务在一个可能丢失的版本上继续写，读者也会读到崩溃后可能消失的数据。反过来，内存落后于磁盘（写 meta 之后失败、未能发布）同样危险，所以此时进入失败状态，只能重开。

bbolt 没有单独的赋值：`db.meta0`、`db.meta1` 直接指向映射中的页 0、页 1，`db.meta()` 每次按 txid 选较新且校验通过的一个。pwrite 写完 meta，映射里立即是新内容，内存发布与写 meta 发生在同一刻，而不是 sync 成功之后。

## meta 字段

| 字段 | 作用 |
| --- | --- |
| magic | 确认文件是本数据库，拒绝任意文件 |
| version | 文件格式版本，格式变化后旧程序拒绝打开而不是读错 |
| pageSize | 创建时决定，打开时核对 |
| root | 根页号 |
| 高水位 | 下一个可分配的页号 |
| txid | 事务号：写入槽位 `txid % 2`，打开时两份都有效则选较大者（见 [文件格式与元数据校验](03-file-format-and-meta-validation.md)） |
| checksum | FNV-64a，覆盖其余全部字段，发现撕裂或损坏的 meta |
| freelist | 空闲页列表位置（E7，尚未加入） |

当前布局（小端，页 0 与页 1 两个槽位相同）：magic `[0,4)`、version `[4,8)`（当前为 2）、pageSize `[8,12)`、保留 `[12,16)`、root `[16,24)`、高水位 `[24,32)`、txid `[32,40)`、checksum `[40,48)`。

## 已知代价：事务内的过时页

每次 put 都立即路径复制。同一事务里后一次 put 会再复制前一次刚生成的页，前一版成为事务内的垃圾，但仍在 dirty 中，提交时照样写盘并占用高水位（尚无页回收）。一次实测（key 约 9 字节、value 50 字节）：

| 一个事务内的 put 次数 | 写出页数 | 新树可达页数 |
| --- | --- | --- |
| 10 | 10 | 1 |
| 100 | 140 | 4 |
| 1000 | 1969 | 33 |

这是逐次路径复制的代价，已由"每张页在一个事务里只复制一次"消除，做法与改进后的数字见 [原子写批次](06-atomic-write-batches.md#事务内的过时页)。

## 工业对照：bbolt

- `tx.page`（[tx.go](../../../references/bbolt/tx.go)）先查事务的 dirty 页表 `tx.pages`，再读 mmap。
- `tx.allocate` → `db.allocate`（[db.go](../../../references/bbolt/db.go)）：先从 freelist 分配，否则取 `rwtx.meta.Pgid()` 并推进高水位；必要时扩大映射。
- `tx.init` 复制 meta 给事务；只读事务与写事务都持有自己的副本。
- `Meta`（[internal/common/meta.go](../../../references/bbolt/internal/common/meta.go)）含 magic、version、pageSize、flags、root、freelist、pgid（高水位）、txid、checksum；`Validate` 依次检查 magic、version、checksum。
- `tx.Commit`（[tx.go](../../../references/bbolt/tx.go)）：`rebalance` 合并过稀节点 → `spill` 把事务中改过的内存节点一次写成页 → 写新 freelist → 高水位上升时 `db.grow` → `tx.write`（排序、pwrite、fdatasync）→ 可选 StrictMode 全树检查，失败即 panic 不发布 → `writeMeta`（写页 `txid % 2`，带校验和，fdatasync）。任何一步失败都 `rollback`（恢复 freelist），没有单独的失败状态。
- `Meta.Write` 在写出前检查根页号与 freelist 页号低于高水位，否则 panic：不发布明显错误的 meta。

## 大纲

- Pager 改为只读映射 + WriteAt 的接口边界（未展开）。
- 扩容：先扩文件长度再映射新 chunk，映射越过文件末尾的访问触发 SIGBUS（未展开）。
- 提交失败后的状态：写 meta 前失败等于回滚，写 meta 后失败只能关闭重开（未展开，见 [提交顺序与同步](04-commit-order-and-sync.md)）。
- 读者借用的 mmap 切片在事务结束、扩容与页回收后的有效期（未展开）。

[本组目录](README.md)
