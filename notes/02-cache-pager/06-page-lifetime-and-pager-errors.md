# mmap 页的借用生命周期与 Pager 错误边界

状态：部分已整理 · 代码：[pager.go](../../pager/pager.go) 的 `ReadPage`、`WritePage`、`Close`、`Grow`；[step2_naive_remap](../../experiments/mmap/step2_naive_remap/main.go)

## 整体重映射为什么会让旧 slice 失效

```go
data, _ := syscall.Mmap(fd, 0, 8192, /* 其余参数略 */)
page0 := data[0:4096]

fp.Truncate(16384)
syscall.Munmap(data)
data, _ = syscall.Mmap(fd, 0, 16384, /* 其余参数略 */)

page0[0] = 'X' // page0 仍保存旧地址，不能再使用
```

`page0` 的 slice header 里存的是旧虚拟地址。解除映射后它不会自动指向新映射，也不会因为 `data` 被赋了新值而更新。旧地址不可访问时会非法访问；地址被复用时，错误可能悄悄写到别的地方。（[mmap(2)](https://man7.org/linux/man-pages/man2/mmap.2.html)）

**Go 的 GC 不管理 mmap 的生命周期。** 映射页的借用必须靠接口约定保证：解除映射后，所有指向它的引用都作废。

在 B+Tree 里这个问题很具体：

```text
1. 读第 42 页，拿到指向映射的 []byte
2. 当前页满了，分配新页准备分裂
3. 分配新页让文件变长，触发整体 munmap + remap
4. 再用第 1 步的 []byte：引用已经失效
```

这就是 [分段映射](05-chunked-mmap-and-pager-ranges.md) 保留旧 chunk 的动机。另一条路是整体重映射，但要保证重映射时没有任何旧视图仍在使用，例如用事务或读写锁划出借用边界：bbolt 的读事务持有 mmap 读锁，重新映射要拿写锁，等所有读者退出（见 [根定位与整树查找](../04-btree-tree/02-root-and-tree-search.md#value-借用还是复制)）。

**一次没崩溃不能证明失效引用合法。** 在 macOS 上曾观察到解除映射后新的 `mmap` 恰好拿到同一个虚拟地址，旧 `page0` 于是没有立即出错；地址安排一变，或地址被别的对象复用，就可能出错或写错位置。`step2_naive_remap` 就是故意使用失效引用的实验，不属于"应该成功退出"的正常流程。

## Go slice 边界与映射、文件边界

```text
Go slice 长度：8192
映射：从文件 offset 0 开始，共 8192 字节
文件逻辑长度：64 字节

data[0]       → 访问文件已有内容
data[0] = 'X' → 共享映射修改
data[5000]    → 在 slice 范围内，但超出文件已有范围
```

三个边界不同：slice 长度决定 Go 会不会报越界；映射范围和文件长度决定这次内存访问能否完成。访问映射内但超出文件末尾的页，Linux 上通常得到 SIGBUS；具体行为还受系统页大小影响，各平台不同。

## Rust 借用能约束什么

```rust
struct Mmap { ptr: *mut u8, len: usize }

impl Mmap {
    fn as_slice(&self) -> &[u8] { /* 生命周期绑定到 &self（示意） */ }
}

impl Drop for Mmap {
    fn drop(&mut self) { /* unsafe { libc::munmap(...) } */ }
}

let slice = {
    let m = Mmap::new(/* ... */);
    m.as_slice()
};                 // m 已被释放
slice[0];          // 编译期拒绝：借用不能活过 m
```

安全封装把视图的生命周期绑定到拥有映射的对象，编译期就能拒绝这种悬垂借用；但前提是封装本身写对，而且它挡不住文件和操作系统的外部变化：

| 问题 | 能否由借用检查解决 |
| --- | --- |
| 映射接口的裸指针有效性 | 需要封装内部保证 |
| 访问时的 OS 错误 | 可能以信号出现，不一定是普通的 `Result` |
| 多进程共享写入 | 进程内借用不能替代跨进程协调 |
| 外部进程截短文件 | 借出的视图挡不住 |
| 文件格式损坏 | 类型系统不代替字节校验 |

所以 Rust 能显著减少悬垂引用，但不能消除 mmap 的全部风险。

## 大纲

- 无效输入（未展开）：页号边界（`ReadPage` 已按文件长度检查）、meta 内容检查、文件长度与乘法溢出、损坏文件处理。
- 扩容失败（未展开）：部分 chunk 建好后的清理、扩文件成功但映射失败时的约定。
- 关闭与借用（未展开）：`Close` 后禁止使用页视图、重复关闭、`Munmap` 出错、并发借用与关闭。
- 文件外部变化（未展开）：外部截短、并发打开和进程间写保护。
- 验证（未展开）：错误注入、进程隔离的故障实验、失败后重开与资源泄漏检查。

下一篇：[页缓存与 buffer pool](07-page-cache-versus-buffer-pool.md) · [本组目录](README.md)。
