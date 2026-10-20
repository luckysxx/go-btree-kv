# 写入、同步与错误边界

状态：部分已整理 · 代码：[experiments/file-api](../../experiments/file-api/)、[pager.go](../../pager/pager.go) 的 `Sync`

## 要解决的问题

`write()` 成功为什么不等于持久化？`fsync()` 同步的是哪些状态？新建的文件为什么还要同步目录？同步失败时，代码可以向调用者承诺什么？

## write、脏页与写回

`write()` 成功不等于数据已到达持久介质。**脏页**表示有修改尚待写回，不表示损坏。

内核可能自动写回，所以没调用 fsync 不等于一定没落盘；但等一会儿或重新读取成功，都不能代替持久化确认。进程退出也不会撤销已成功写入的内容（见 [页缓存与进程退出](02-data-visibility-durability.md#页缓存为什么能跨越进程退出)）。

## fsync 能保存什么

设 OLD 是文件里的旧数据，NEW 是程序准备的新数据：

- NEW 若只在进程的私有缓冲区里，fsync 不能把它写进文件。fsync 处理的是文件系统**已经知道**的状态，不会替程序完成还没执行的 write。
- fsync 不要求无条件重写 OLD；没有待同步的工作时它可能很快返回。

## 父目录的同步

### 文件的内容与文件的名字分开存放

文件系统里，一个文件由两部分组成：

- **inode 与数据块**：文件本身，包括内容、大小、修改时间等元数据。
- **目录项**：目录也是盘上的一个文件，内容是一张"名字 → inode 号"的表。一个文件的名字，就是某个目录里的一项。

新建 `db/00008.sst` 并写入内容，要改两处：分配一个 inode 并写进数据；在 `db/` 这个目录的表里加一项 `00008.sst → 这个 inode`。两处都先改在内存的页缓存里，各自找时机写回磁盘。用 C 来想：目录像一个存在磁盘上的 `map<名字, inode>`，inode 像 map 指向的一个 struct，写文件改的是 struct，新建、改名、删除改的是 map。

### fsync 只同步传给它的那个对象

`fsync(fd)` 同步的是这个 fd 指向的 inode：数据与这个 inode 的元数据（包括文件长度）。它不保证包含这个文件的目录项也已落盘，要保证这一点，还要对**目录的 fd** 再调用一次 fsync。（[Linux fsync](https://www.man7.org/linux/man-pages/man2/fsync.2.html) 的 NOTES 一节）

只同步了文件、没有同步目录时，断电之后可能出现：数据完好地在磁盘上，却没有任何名字指向它。重启后文件不见了，空间被文件系统回收，或者出现在 `lost+found` 里。

同步目录的写法是以只读方式打开目录本身，再对它 fsync：

| 语言 | 写法 |
| --- | --- |
| C | `int dfd = open("db", O_RDONLY \| O_DIRECTORY); fsync(dfd); close(dfd);` |
| Go | `d, _ := os.Open("db"); d.Sync(); d.Close()` |
| Rust | `File::open("db")?.sync_all()?;` |

这和程序里有几个目录无关：只要某个目录的内容（它的名字表）变了，就要同步这个目录。只有一个数据目录的程序，每新建一个文件，这个目录就变一次。

### 哪些操作改目录

| 操作 | 改不改目录 | 要不要同步目录 |
| --- | --- | --- |
| 新建文件 | 改：加一项 | 要，在文件内容同步之后 |
| 写入或追加已有文件 | 不改：名字没变，文件变长属于 inode | 不要，同步文件本身即可 |
| rename | 改：一项换了名字，替换时还去掉了旧的那一项 | 要；跨目录的 rename 两个目录都要同步 |
| 删除文件（unlink） | 改：去掉一项 | 看删除没落盘的后果：文件"复活"若无害，可以不同步 |

所以"新建一个日志文件，之后一直追加"只在新建时同步一次目录；"写临时文件再 rename 替换"每次替换都要同步目录（见 [临时文件与原子替换](05-atomic-file-replacement.md)）。

### 常见错误

- **只同步文件就当作文件已经持久**：新建的文件在正常运行和进程被杀时都看不出问题，只有整机断电才会暴露，测试很难发现。
- **在文件内容同步之前同步目录**：断电后名字在、内容不完整。顺序是先同步文件，再同步目录。
- **依赖文件系统的实现细节**：一些文件系统在同步文件时顺带提交了目录项（例如 ext4 的日志一次提交了两者），但这不是接口的保证，换文件系统或挂载选项就可能不成立。

### 工业对照

- **etcd 的 WAL**：`WAL` 结构体长期持有一个目录的 fd（`dirFile`，注释是 "for syncing on Rename"）。切换到新的日志段时，先写好并同步临时文件，rename 成正式的段名，再 `fileutil.Fsync(w.dirFile)`。新建 WAL 目录时整个目录是 rename 过去的，之后同步的是它的**父目录**（`server/storage/wal/wal.go` 的 `cut`、`Create`）。
- **LevelDB**：不在每个新 SST 之后单独同步目录，而是每次同步 MANIFEST 时顺带同步一次数据库目录（POSIX 环境的 `SyncDirIfManifest`）。新 SST 总是先于引用它的 MANIFEST 记录写好，所以这一次目录同步把之前新建的 SST 的名字也一起落了盘。
- **RocksDB**：flush 与 compaction 写出新文件后同步数据库目录；`CURRENT` 用"写临时文件再 rename"替换，替换之后同步目录。

### LSM 中的对应

- **新 SST**：写完同步文件，再同步数据库目录，之后才能写进 MANIFEST。本项目的 Rust LSM 在 `lsm/engine/src/lsm_storage.rs` 的 `sync_dir`，flush 与 compaction 写完新文件后各调用一次。
- **MANIFEST**：只在第一次创建时同步目录，之后每条记录只同步 MANIFEST 本身。
- **CURRENT**：每次换 MANIFEST 都是一次 rename，要同步目录。
- **删除旧 SST**：可以不同步目录。删除没落盘，文件会作为孤儿重新出现；它不在 MANIFEST 里，打开数据库时被当作残留删掉。

## 平台与设备

设备还可能有易失的写缓存。Linux 的 fsync 要求同步文件数据和相关元数据，并等待设备完成（包括必要的缓存刷新）。（[Linux fsync](https://www.man7.org/linux/man-pages/man2/fsync.2.html)）

macOS 要区分 fsync 和 `F_FULLFSYNC`，后者进一步要求设备刷新内部缓存。（[Apple fsync](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html)）

所以"fsync 把脏页刷到磁盘"只是简写：一次 `fp.Sync()` 不等于跨平台的通用断电保证，还要看平台、文件系统和设备。

## 同步失败后的承诺

同步失败或尚未完成时，不能承诺恢复后一定是完整的 OLD 或完整的 NEW。同步成功也不会冻结之后的修改，更不会把多个文件操作自动变成一个事务。

## 大纲

- 短写与完整写入循环（未展开）：返回的字节数、错误与完整写入条件；部分完成时调用方怎样继续或停止。
- Write、Sync、Close 的错误传播（未展开）：各步错误如何返回；同步失败后的对象状态与关闭；与临时文件清理、提交边界的连接。
- 故障实验（未展开）：可注入写入、同步、关闭失败的最小实验与预期检查。

## 相关

后续：[临时文件与原子替换](05-atomic-file-replacement.md)。返回 [本组目录](README.md)。
