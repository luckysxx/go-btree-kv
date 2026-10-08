# 写入、同步与错误边界

状态：部分已整理 · 代码：[experiments/file-api](../../experiments/file-api/)、[pager.go](../../pager/pager.go) 的 `Sync`

## 要解决的问题

`write()` 成功为什么不等于持久化？`fsync()` 同步的是哪些状态？同步失败时，代码可以向调用者承诺什么？

## write、脏页与写回

`write()` 成功不等于数据已到达持久介质。**脏页**表示有修改尚待写回，不表示损坏。

内核可能自动写回，所以没调用 fsync 不等于一定没落盘；但等一会儿或重新读取成功，都不能代替持久化确认。进程退出也不会撤销已成功写入的内容（见 [页缓存与进程退出](02-data-visibility-durability.md#页缓存为什么能跨越进程退出)）。

## fsync 能保存什么

设 OLD 是文件里的旧数据，NEW 是程序准备的新数据：

- NEW 若只在进程的私有缓冲区里，fsync 不能把它写进文件。fsync 处理的是文件系统**已经知道**的状态，不会替程序完成还没执行的 write。
- fsync 不要求无条件重写 OLD；没有待同步的工作时它可能很快返回。

## 平台、设备与父目录

设备还可能有易失的写缓存。Linux 的 fsync 要求同步文件数据和相关元数据，并等待设备完成（包括必要的缓存刷新）；文件的新建与重命名还涉及**父目录**的同步。（[Linux fsync](https://www.man7.org/linux/man-pages/man2/fsync.2.html)）

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
