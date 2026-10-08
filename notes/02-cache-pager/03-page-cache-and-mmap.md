# 页缓存、共享映射与写回

状态：已有正文 · 代码：[mmap 基础实验](../../experiments/mmap/step1_mmap_basic/main.go)、[pager.go](../../pager/pager.go) 的 `WritePage`、`Sync`

## 两种访问方式

传统读写：

```go
buf := make([]byte, 4096)
fp.ReadAt(buf, 42*4096) // 文件内容读入用户缓冲区
buf[0] = 0xff
fp.WriteAt(buf, 42*4096) // 把修改交给文件写入路径
```

共享文件映射：

```go
data, _ := syscall.Mmap(fd, 0, fileSize,
    syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
page42 := data[42*4096 : 43*4096]
page42[0] = 0xff
```

（省略了错误处理。）mmap 让调用方直接通过地址访问映射内容，不必每次显式 `ReadAt`，也不必复制到自己的缓冲区。缺页机制见 [上一篇](02-virtual-memory-and-page-faults.md)。

一个比喻：read/write 像请馆员复印一份给你，mmap 像拿到仓库钥匙直接看书、改书。它只区分访问方式，不说明修改已进入持久介质。"零系统调用、零拷贝"也只是相对显式缓冲区读写的简化：建立与解除映射仍要系统调用，访问还可能缺页。

## MAP_SHARED 与 MAP_PRIVATE

| 模式 | 含义 |
| --- | --- |
| `MAP_SHARED` | 修改属于共享的文件映射，映射同一区域的其他进程可见，并可写回文件 |
| `MAP_PRIVATE` | 私有写时复制：修改不写回底层文件，也不对其他映射可见 |

（[mmap(2)](https://man7.org/linux/man-pages/man2/mmap.2.html)）例如把映射改成 `MAP_PRIVATE` 后修改 `data[0]`，再用 hexdump 看文件，内容不变：修改只在进程的私有副本里。

## 脏页与写回

共享映射写入后，相关页变脏，内核可以在后台写回。显式同步用于提出持久化要求。修改、脏页、写回是三个阶段，但 MMU 的脏位、内核写回线程与同步调用的范围不是同一层概念；具体的写回周期与线程因内核版本而异。

经可写共享映射修改页，就等于把页交给内核随时写回，数据库无法控制顺序。本项目的 Pager 因此只读映射，写入走 `WriteAt`，何时 `Sync` 由上层提交协议决定（见 [提交顺序与同步](../05-storage-recovery/04-commit-order-and-sync.md)）。

## 看得见不等于已经持久

注释掉同步后，`cat` 或 `hexdump` 仍可能立即读到更新，因为读取命中了仍在运行的系统中的页缓存；写入进程退出也不会让页缓存消失（完整对照见 [数据可见性与持久性](../01-file-persistence/02-data-visibility-durability.md)）。

关键结论：**mmap 改变的是访问方式，不自动提供提交与恢复协议。** 没有完成同步时，故障后的结果没有保证；但也不能写成"断电必然丢"，因为后台写回可能已经发生。正常关闭重开、进程终止、整机故障是不同的实验条件，要分别验证。

## 实验

基础实验默认只在合法范围内修改两个字节，不主动同步；`--sync` 模式调用 `fp.Sync()`，`--bounds-failure` 单独演示 Go 的越界 panic。它们观察的是可见性和同步调用，不是断电实验。运行方式见 [实验入口](../../experiments/README.md)。

下一篇：[文件扩容与稀疏文件](04-file-growth-and-sparse-files.md) · [本组目录](README.md)。
