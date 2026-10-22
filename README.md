# go-btree-kv

A learning-oriented, single-file key-value store in Go, built on a copy-on-write B+Tree over a page-based pager (read-only chunked mmap for reads, `pwrite` for writes). Writes go through serializable single-writer transactions, and readers get snapshot reads. Commits are atomic: dirty pages are written, then fsync, then one of two checksummed meta pages is written, then fsync again. Pages freed by a commit are reclaimed only after every reader that might still see them has finished. Recovery is tested by killing a child process with SIGKILL at every step of a commit. Design notes, written in Chinese, cover everything from file APIs and the page cache to commit order and recovery. Reads and writes follow bbolt's approach, but the code is written from scratch. This is a study project and is not meant for production use.

一个学习用的单文件 KV 存储，用 Go 写：在按页读写的 Pager 之上实现写时复制（CoW）的 B+Tree，再加上事务、提交与恢复、页回收。配套的笔记从文件 API 讲起，一直讲到可恢复的单机 KV。设计上对照 bbolt，代码是自己写的。不用于生产。

## 现在能做什么

对外的接口在 [`btree/kv.go`](btree/kv.go)：

```go
db, err := btree.Open("data.db")
defer db.Close()

err = db.Put([]byte("k"), []byte("v"))          // 返回时已经持久
v, found, err := db.Get([]byte("k"))            // 返回副本
found, err = db.Delete([]byte("k"))
err = db.Scan(start, end, func(k, v []byte) error { ... })

err = db.Update(func(tx *btree.Tx) error { ... }) // 一个写事务：一起生效，或者都不生效
err = db.View(func(tx *btree.Tx) error { ... })   // 只读事务：看到开始时的快照
```

- **Pager**（[`pager/`](pager/pager.go)）：
  - 文件按 4 KB 一页读写。读走只读的 mmap，写走 `WriteAt`，何时 fsync 由上层决定。
  - 映射按段（chunk）追加：文件变长时，只给新增的部分建映射，换上新的映射列表。读者从不等待扩容，已经拿到的页也一直有效。
- **CoW B+Tree**（[`btree/tree.go`](btree/tree.go)）：
  - 修改时从根到叶子复制一条路径，旧版本原样保留，正在读的人看到的始终是完整的旧树。
  - 一个事务里每张页只复制一次，同一事务的多次写入不会反复复制。
  - 支持叶子与内部页的分裂、删除时摘掉空页、根的分裂与收缩，以及按 key 范围扫描的游标。
- **事务与提交**（[`btree/tx.go`](btree/tx.go)、[`btree/db.go`](btree/db.go)）：
  - 同一时刻只有一个写事务，读事务可以有很多个，写事务不挡读事务。
  - 提交的顺序：写脏页 → fsync → 写 meta → fsync。
  - meta 有两个槽位，轮流写入，带事务号与 FNV-64a 校验和。打开时选校验通过、事务号较大的那份，提交写到一半崩溃就退回上一版。
- **页回收**：被替换下来的页先记在“待回收”表里，等所有可能还看得到它的读事务结束后，才放进空闲清单重用。空闲清单只在内存里，打开时从根出发重算一遍。
- **故障处理**：写 meta 之后的步骤出错，数据库进入失败状态，拒绝后续写入，要求关闭后重开。不会把“可能已经落盘、也可能没有”的提交当作失败报告给调用者。
- **工具**：[`cmd/dbdump`](cmd/dbdump/main.go) 只读地查看文件里每一页的页头、meta 与字节。

## 设计要点与 bbolt 的对照

读写方式与 bbolt 相同：只读 mmap 加 `pwrite`，两份 meta 交替写，FNV-64a 校验和，读事务登记、待回收表。不同的地方：

- **映射怎样扩容**：bbolt 在文件变长时重新映射整个文件，要拿锁等所有读事务让开。这里按段追加映射，用原子指针换上新的映射列表，读者不等待（[笔记](notes/02-cache-pager/05-chunked-mmap-and-pager-ranges.md)）。
- **空闲清单**：bbolt 把 freelist 写进文件（也可以选不写，打开时扫描）。这里只用后一种做法：打开时从根出发找出不可达的页，所以打开的耗时随文件大小增长（[笔记](notes/05-storage-recovery/07-page-reclamation-and-readers.md)）。
- **删除**：bbolt 提交时合并过稀的节点。这里只摘掉删空的页，不合并、不借位，删除后叶子可能很稀疏（[笔记](notes/04-btree-tree/06-delete-rebalance-and-root-shrink.md)）。
- **大的 value**：bbolt 用溢出页（overflow page）存放放不进一页的记录。这里一条记录必须放进一张空叶子（key 最长 1000 字节），否则返回 `ErrRecordTooLarge`。
- **只有一棵树**：没有 bbolt 的 bucket（嵌套的命名空间），整个文件是一个按字节序排列的 key 空间。

## 快速开始

需要 Go 1.26（见 [go.mod](go.mod)）。

```sh
go test ./...                          # 含进程崩溃测试，btree 包约半分钟
go test -race -short ./pager ./btree   # 竞争检测
go vet ./...
```

查看一个数据库文件（选项放在文件名之前）：

```sh
go run ./cmd/dbdump --bytes 64 path/to/data.db
```

文件 API 与 mmap 的小实验见 [experiments](experiments/README.md)：每次运行在独立的临时目录里生成文件，可以用 `hexdump` 或 `dbdump` 查看。

## 代码地图

| 目录 | 作用 | 读代码的入口 |
| --- | --- | --- |
| [pager](pager) | 按页读写文件：分段只读映射、`WriteAt`、`Sync`、扩容、关闭 | [pager.go](pager/pager.go) |
| [btree](btree) | 页内操作、整树（查找、插入、删除、游标、全树检查）、事务、提交与恢复、对外 API | [函数地图](btree/README.md)、[kv.go](btree/kv.go)、[db.go](btree/db.go)、[tx.go](btree/tx.go)、[tree.go](btree/tree.go) |
| [cmd/dbdump](cmd/dbdump) | 只读查看文件中的页与字节 | [main.go](cmd/dbdump/main.go) |
| [experiments](experiments) | 文件 API、整文件原子保存、mmap 与扩容的小实验 | [说明](experiments/README.md) |
| [notes](notes) | 设计笔记（中文） | [阅读索引](notes/README.md) |

测试的分层：
- **页内算法**：表驱动的单元测试。
- **整树**：和一个简单的有序表对照的模型测试，每次修改后做全树检查（[check.go](btree/check.go)）。
- **事务与恢复**：在提交的各个步骤注入写入失败。
- **进程崩溃**（[crash_test.go](btree/crash_test.go)）：子进程在提交的指定步骤用 SIGKILL 杀掉自己，父进程重开文件，按确认协议检查读到的是第 k 或第 k+1 个版本。

## 设计笔记

[notes/](notes/README.md) 分六组，前五组按搭建这个引擎的顺序排列：

| 组 | 内容 |
| --- | --- |
| A 文件、可见性与持久化 | 文件 API、数据在哪里、`write` 与 `fsync`、整文件原子替换、hexdump |
| B 缓存、映射与 Pager | 虚拟内存、页缓存与 mmap、文件扩容与分段映射、页的生命周期 |
| C B+Tree 页内算法 | 搜索结构、节点格式与变长记录寻址、叶子与内部页的插入和分裂 |
| D 完整 B+Tree | 页存储、查找、路径复制、根分裂与发布、游标、删除、全树检查 |
| E 单机提交、恢复与空间管理 | 原地更新与 CoW、提交顺序与同步、打开与崩溃恢复、原子写批次、页回收与读者、单机 KV API |
| K 存储引擎深入对照 | 页内布局、长 key、B+Tree 与 LSM、MVCC |

可以先读这几篇：

- [提交顺序与同步](notes/05-storage-recovery/04-commit-order-and-sync.md)：为什么是“脏页 → fsync → meta → fsync”，少一次 fsync 会出什么错。
- [打开与故障恢复](notes/05-storage-recovery/05-open-and-crash-recovery.md)：两份 meta 怎样选，以及进程崩溃测试的确认协议。
- [页回收与读者](notes/05-storage-recovery/07-page-reclamation-and-readers.md)：旧页什么时候才能重用。
- [B+Tree 与 LSM](notes/06-storage-comparisons/03-btree-and-lsm.md)：两种引擎的取舍。LSM 的实现在另一个仓库 [rust-lsm](https://github.com/luckysxx/rust-lsm)。

## 已知问题与限制

**持久性的验证范围**

- 只验证了进程崩溃（SIGKILL），页缓存还在；没有验证断电，也没有在 Pager 的系统调用层注入 I/O 错误。
- 数据页没有校验和，只有 meta 有。数据页损坏（位翻转、写坏）发现不了，读到的可能是错误的数据，也可能被当作树的结构错误报出来。

**功能与空间**

- 不支持大的 value：一条记录必须放进一页。
- 删除不合并节点，大量删除后空间利用率低；文件只增不缩，空闲页只在文件内部重用。
- 空闲清单不落盘，打开时要扫描整棵树。
- 两份 meta 只保证能退回上一版：上一版用到、这一版不再用的页，打开时会被算作空闲。

**性能**

- 没有基准测试，也没有和 bbolt 比较过吞吐与延迟。
- 每次提交至少两次 fsync，没有 group commit。单线程逐条 `Put` 的速度受 fsync 限制，要快就把多次写入放进一个 `Update`。
- 映射段的大小是演示值 16 KB（`pager.ChunkSize`），大文件会建很多个映射；实际使用时应当取大得多的段。

## 后续

这个引擎不再加新功能，存储方面的工作在 [rust-lsm](https://github.com/luckysxx/rust-lsm) 里继续。上面的已知问题会按需修，最先做的是数据页校验和与 Pager 层的 I/O 故障注入。

## 许可证

本项目以 [Apache License 2.0](LICENSE) 发布。设计上对照 [bbolt](https://github.com/etcd-io/bbolt)（MIT），没有复制它的代码。
