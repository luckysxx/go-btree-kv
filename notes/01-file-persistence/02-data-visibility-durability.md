# 数据状态：位置、可见性与持久性

状态：已有正文（范围：本地普通文件）

## 要解决的问题

"写入成功""其他进程读到了""重启程序还在""断电后还能恢复"分别说明什么？

设文件里已持久保存 OLD，进程 A 要改成 NEW，进程 B 在 A 写完后重新读取同一文件（没有后续修改，读写先后已协调，B 不使用自己缓存的旧结果）。

## 三个维度

- **位置**：最新版本在进程私有缓冲区、内核页缓存、设备缓存，还是持久介质？
- **可见性**：观察者通过什么途径读——私有变量、文件读取还是共享映射？
- **持久性**：面对进程退出、系统崩溃或整机断电，哪些已确认的修改还能恢复？

同一份数据可以同时有多个副本，版本也可以不同；数据进入下一层，不意味着上一层的副本消失。

## 一个典型过程

```text
程序私有缓冲区
    │ write(fd, data)
    ▼
内核管理的文件状态 / 页缓存
    │ 写回与持久化同步（还要考虑设备与平台要求）
    ▼
持久存储
```

| 状态 | B 重新读文件 | 只有 A 退出，系统继续运行 | 系统崩溃 / 断电 |
| --- | --- | --- | --- |
| NEW 仅在 A 的私有缓冲区 | OLD | NEW 丢失 | NEW 丢失 |
| 已成功 write，但未确认持久化 | NEW | 保留：内核中的修改不会因 A 退出而撤销 | 不保证 NEW 保留 |
| 完成符合平台要求的持久化同步 | NEW | 保留 | 在相应文件系统与设备的保证下保留 |

对成功 write 覆盖的字节，之后的读取应得到新内容，直到这些位置再被修改。**可见性不需要先完成 fsync。**（[POSIX write](https://pubs.opengroup.org/onlinepubs/9699919799/functions/write.html)）同步的作用范围见 [写入、同步与错误边界](04-write-sync-and-errors.md)。

## 页缓存为什么能跨越进程退出

页缓存由内核管理，不属于 A 的私有内存。A 写完后即使后台尚未开始写回，A 退出也不会让这些缓存页消失。

所以 **"杀掉 A 再启动程序还能读到 NEW"不能证明断电安全**：新程序读到的可能只是内核页缓存。整机断电会丢失普通 RAM 内容，包括页缓存。判断时要问的是最新数据所在的存储是否易失，而不只是"后台任务还活着吗"。

用词：这里说的是"写回持久存储"，不是"持久化内存"（后者容易与持久内存硬件混淆）；内核也不是一个普通的系统进程。

## 后台写回由谁执行

内核代码同样由 CPU 执行：系统调用可以在调用线程的内核态运行，中断、异常和独立的内核线程也可以执行内核代码。Linux 的工作队列把后台任务交给工作线程，线程获得调度后执行。周期检查与脏数据阈值会触发写回，写得太快还可能被限速。（[工作队列](https://kernel.org/doc/html/latest/core-api/workqueue.html)、[写回参数](https://kernel.org/doc/html/latest/admin-guide/sysctl/vm.html)）

因此"进程退出"与"易失数据丢失"要分开判断。

## mmap 在哪里

文件的 `MAP_SHARED` 映射允许通过虚拟地址修改共享的文件页。其他共享映射的使用者在正确协调访问后能观察到修改，但**共享可见性不是持久化保证**，也不代替线程或进程间的同步。`MAP_PRIVATE` 的修改不会写回原文件。映射修改的同步手段包括 `msync(MS_SYNC)`，它与 `fsync` 如何配合需要按平台核实。（[mmap](https://www.man7.org/linux/man-pages/man2/mmap.2.html)、[msync](https://man7.org/linux/man-pages/man3/msync.3p.html)）映射与 Pager 见 [缓存、映射与 Pager](../02-cache-pager/README.md)。

## 与项目代码的联系

- [保存实验](../../experiments/atomic-save/main.go)：write、同步文件、rename 各做什么；完整保存协议还需要同步父目录，见 [临时文件与原子替换](05-atomic-file-replacement.md)。
- [Pager](../../pager/pager.go)：`ReadPage` 返回只读映射中的页，`WritePage` 用 `WriteAt` 写入页缓存，`Sync` 调用文件同步。提交顺序由 [db/tx](../../btree/tx.go) 负责（见 [提交顺序与同步](../05-storage-recovery/04-commit-order-and-sync.md)）。
- 文件被误截短属于一次新的修改，之前同步过旧内容也挡不住它（见 [Pager 文件长度与映射范围](../02-cache-pager/05-chunked-mmap-and-pager-ranges.md)）。

## 相关

[写入、同步与错误边界](04-write-sync-and-errors.md) · [原子性与并发](03-atomicity-and-concurrency.md) · [本组目录](README.md)。
