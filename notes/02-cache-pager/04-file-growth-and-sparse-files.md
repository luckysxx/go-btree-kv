# 文件扩容、逻辑长度与稀疏占用

状态：已有正文 · 代码：[pager.go](../../pager/pager.go) 的 `growMmap`

## 文件怎样长大

```go
// 追加零字节：经过用户缓冲区和实际写入调用
fp.Seek(0, io.SeekEnd)
fp.Write(make([]byte, 4096))

// 调整文件长度：当前 Pager 使用这条路径
fp.Truncate(newSize)
```

还有 Linux 的 `fallocate` 空间预分配接口，本项目没有使用。

`Truncate` 的语义是**设置新的文件长度**：缩短会丢弃尾部，增大时新增范围读作零。它不是"只能扩容"——这正是 [重开误截短](05-chunked-mmap-and-pager-ranges.md#失败例重开时误截短) 的根源。（[truncate(2)](https://man7.org/linux/man-pages/man2/truncate.2.html)）

"写零最慢、Truncate 零 I/O 只改 inode、fallocate 介于两者之间"这类排序过于简化，性能要在具体平台上测。

## 文件长度与磁盘占用是两个量

稀疏文件：逻辑长度可以很大，某些范围还没分配数据块，这些范围叫**洞**。读洞得到零；这是内容上的观察，不能推出"底层完全没有 I/O"。

```text
文件 /tmp/db.data
逻辑长度：1 GiB
实际分配：可能远小于 1 GiB

Truncate 到 1 GiB        → 可读范围扩大
在约第 5 亿字节处写入    → 该范围可能需要分配数据块
```

数据库可以先扩长文件，让之后的映射有足够的文件范围；物理分配何时发生取决于系统与文件系统。Pager 的 `fileSize` 表示逻辑长度，不表示已经预分配磁盘空间。

## 观察命令

```bash
ls -lh file    # 逻辑长度
du -h file     # 实际占用
stat file     # 大小、块计数等；各平台输出不同

# 观察逻辑文件中远处的一段字节
dd if=/tmp/db.data bs=1 count=100 skip=500000000 | xxd

# 造一个 1 GiB 的稀疏文件（dd 对大小后缀的支持因平台而异）
dd if=/dev/zero of=/tmp/sparse bs=1 count=0 seek=1G
ls -lh /tmp/sparse    # 约 1.0G
du -h /tmp/sparse     # 约 0
```

运行实验应使用独立的临时路径。

## 平台差异

在 macOS/APFS 上曾观察到：一个名义约 480 KiB、只显式写入 3 字节的文件，`ls -lh` 和 `du -h` 都显示约 480 KiB，而同类文件在 Linux ext4 上的占用通常小得多。原因可能与 APFS 的稀疏支持或 `MAP_SHARED | PROT_WRITE` 映射有关，但尚未核实。这类观察要记录完整环境（机器、系统版本、文件系统、操作序列）才能比较；不能用一台机器上的占用现象代替目标环境的容量与性能测量。

## 失败例

把大文件 `Truncate` 到更小长度后，再扩大只能得到新的零字节范围，**不能恢复已删除的尾部**。下一篇把这个失败例落到 Pager：文件已经够大时仍可能需要增加映射，两种需求不能合并成一个判断。

下一篇：[分段映射与 Pager 的三个范围](05-chunked-mmap-and-pager-ranges.md) · [本组目录](README.md)。
