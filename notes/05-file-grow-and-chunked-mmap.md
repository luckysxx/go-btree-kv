# 笔记 05：文件 grow + 分段 mmap

> 数据库本质是"文件要长大"。mmap 给的 slice 长度在调用那一刻就锁死，
> 文件涨大就必须重新 mmap。这一篇讲怎么扩文件、怎么扩映射、怎么避免
> 用 mmap 写存储引擎最经典的 bug：野指针。

---

## 1. 怎么让文件变大？三种办法

### 方法 1：`write` 追加零数据
```go
fp.Seek(0, io.SeekEnd)
fp.Write(make([]byte, 4096))
```
能用但要分配 + 写一块零。**最慢**。

### 方法 2：`Truncate`（**首选**）
```go
fp.Truncate(newSize)
```
**零 IO**：只改 inode 里的 size 字段，不分配磁盘 block、不写任何字节。

### 方法 3：`fallocate`（Linux 专属）
预分配磁盘空间但不写零。介于 1 和 2 之间。macOS 没有，本书不用。

---

## 2. Sparse File：Truncate 为什么"免费"

文件有两个独立指标：

```
文件 /tmp/db.data:
  size = 1073741824          ← 名义大小：1GB
  blocks_allocated = 16       ← 实际占磁盘 block：16 个 = 64KB
```

**`Truncate(1GB)` 只改 `size`**，不分配 block。多出来的部分是"洞"。

### 读"洞"会怎样？

文件系统直接返回零，**不碰磁盘**：
```bash
# 名义 1GB 文件，实际只占 64KB
dd if=/tmp/db.data bs=1 count=100 skip=500000000 | xxd
# → 全是 00 00 00 ...
```

### 什么时候才真正占磁盘？

**实际写入**某一页时，文件系统才分配那一页的 block：
```
Truncate(1GB)              → size=1GB, blocks=16
write @ offset 5亿处        → size=1GB, blocks=24  (新分配那 4KB)
```

### 验证命令

```bash
ls -lh file    # 看名义 size
du -h  file    # 看真实磁盘占用
stat   file    # 看 blocks_allocated 等元信息
```

→ 数据库利用这点：**预先 Truncate 一大块空间**让 mmap 能映射这段范围，
真实磁盘分配摊到每次实际写入页时。

---

## 3. mmap 不能"原地变长"

```go
data, _ := syscall.Mmap(fd, 0, 8192, ...)
// data 长度永远 = 8192，文件涨多大都没用
fp.Truncate(16384)
data[10000]   // ❌ Go panic: index out of range
```

唯一办法：`munmap` 旧的 + `mmap` 新的。

---

## 4. 致命陷阱：旧 slice 变野指针 ☠️

```go
data, _ := syscall.Mmap(fd, 0, 8192, ...)
page0 := data[0:4096]                // ← 拿到 page 0

fp.Truncate(16384)
syscall.Munmap(data)                  // 解除旧映射
data, _ = syscall.Mmap(fd, 0, 16384, ...) // 重新映射 → 可能完全不同的虚拟地址

page0[0] = 'X'                        // ☠️ SIGSEGV
```

**为什么炸？** (回顾笔记 04)
- `page0` 是个 slice header，里面记着**旧的虚拟地址**
- `Munmap` 把那段虚拟地址的页表项**删了**
- 新 `mmap` 大概率分配到不同虚拟地址
- 访问 `page0` = 访问已不在页表里的虚拟地址 → SIGSEGV

→ **mmap 返回的 slice 有生命周期，munmap 之后所有指向它的引用全部作废**。
Go runtime 不知道，不会帮你 GC，也不会拦——是"非 Go 风险点"。

### 在 B+Tree 里有多致命

```
B+Tree insert:
  1. GetPage(42)  → []byte（指向 mmap 内）
  2. 当前页满 → 要分裂 → NewPage()
  3. NewPage 触发 grow → munmap + remap
  4. 回头改步骤 1 拿到的 []byte → 💥
```

整个 B+Tree 到处持有指向 mmap 的 slice，任何一次申请新页都可能让它们失效。
BoltDB 早期真的踩过这个坑。

---

## 5. 解决方案：分段映射 (chunked mmap)

**核心思路**：不要一次 mmap 整个文件，按段（如 64MB）分别 mmap。
文件涨大时**只追加新 chunk**，老 chunks 不动。

```
文件:       [─────────────────────────────────]
             ↑          ↑          ↑
mmap 区块:  [chunk 0]  [chunk 1]  [chunk 2] ...
             64MB       64MB       64MB

grow 之前: chunks = [chunk0]
grow 之后: chunks = [chunk0, chunk1]   ← chunk0 完全没动
            → 老的 page slice 仍然有效 ✅
```

### `GetPage(N)` 实现

```go
chunkIdx := N * 4096 / chunkSize
offsetIn := N * 4096 % chunkSize
return chunks[chunkIdx][offsetIn : offsetIn+4096]
```

代码稍复杂，但**老 slice 永不失效**。本书选这条。

### 备选方案：整体 remap (BoltDB / LMDB)

每次 grow munmap + 重新 mmap 整个文件。**调用方负责确保此时没有任何旧 slice 在用**。
靠事务边界 + 全局写锁来保证。代码简单但每次操作要小心。

---

## 6. 本书 Pager 策略（写代码照搬）

1. 文件 grow 用 `Truncate`（稀疏文件，零 IO）
2. mmap 分段：维护 `chunks [][]byte`，每段 64MB
3. 文件超过当前总映射大小时，追加一个新 chunk（额外 mmap 调用）
4. `GetPage(N)` 算出落在哪个 chunk，返回那段的子 slice

---

## 7. Rust 视角：能完全解决野指针吗？

**部分能，但本质不行**。

### Rust 能拦住的

生命周期系统把 slice 的有效期绑在 `Mmap` 对象上：

```rust
struct Mmap { ptr: *mut u8, len: usize }
impl Mmap {
    fn as_slice(&self) -> &[u8] { /* 生命周期绑 &self */ }
}
impl Drop for Mmap {
    fn drop(&mut self) { unsafe { libc::munmap(...) }; }
}

// 下面代码编译不过：
let slice = {
    let m = Mmap::new(...);
    m.as_slice()       // 借用 m
};                     // m drop → munmap
slice[0];              // ❌ 编译错误：悬垂引用
```

→ C/Go 在运行时 SIGSEGV，Rust 在**编译期**就拦住。真实胜利。

### Rust 拦不住的

| 问题 | 为什么 Rust 无能为力 |
| ---- | -------------------- |
| mmap 本身要 unsafe | 内核给的裸指针，Rust 不知道它是否真有效 |
| 磁盘 IO 错误 = SIGBUS | 是 OS 信号，绕过所有 Rust 机制 |
| 多进程同 MAP_SHARED 文件并发写 | Rust 的 `&mut` 只在进程内有效，跨进程靠不住 |
| 文件被外部 truncate 缩小 | 已映射区域指向不再存在的文件部分 → SIGBUS |
| 文件格式损坏 | Rust 类型系统不知道字节布局，强转出垃圾值 |

### 工业实践
- **sled** (Rust)：核心走 unsafe + 自己安全封装
- **heed / lmdb-rs**：本质包装 C 的 LMDB，内部全是 unsafe

### 结论
> Rust 把野指针 bug 大量挪到编译期发现，**显著减少**事故，但**不能消除**。
> mmap 本质是"借内核的内存"，语义上就在 Rust 安全模型之外。

→ 用 Go 学懂这套之后，用 Rust 重写一遍是绝佳练习——
你会在每一处必须 `unsafe` 的地方更深理解"系统编程"的边界。

---

## 8. 实测踩到的几个坑（macOS 实验观察）

### (a) macOS XNU 经常给 munmap 后的新 mmap **同一个虚拟地址**
旧 `page0` 指针误打误撞还指向有效内存，"野指针 bug" **不崩**。
→ 这是这类 bug 最阴险的形式：
- 测试时没崩
- 内核策略一变 / 负载一变就崩
- **崩之前可能已经写到错误位置，悄无声息损坏文件**

结论：**不能依赖"测试通过 = 安全"**。分段映射是工程上的必然选择。

### (b) 分段映射后，chunk 之间的虚拟地址常常**不连续**
观察到：chunk 0~3 紧邻 (差 16KB)，chunk 4 跳了 ~600MB。
原因：连续虚拟地址段被 Go runtime / stdlib / 堆等"挡路"了，
内核换一块空地继续分配。

→ **正是分段映射的好处**：每个 chunk 独立 mmap，不要求虚拟地址相邻；
   拼接靠算术 (`chunkIdx + inChunk`)，不靠地址连续。
→ 整体 mmap 方案在文件涨到几 GB 时会找不到连续虚拟地址段，分段连这个都顺手解决了。

### (c) macOS APFS 的 sparse file 支持比 Linux ext4 **弱**
实测一个名义 480K、只写过 3 字节的文件：
```
ls -lh : 480K
du  -h : 480K   ← 在 Linux ext4 上通常显示 ~8K
```
原因：**MAP_SHARED + PROT_WRITE 的 mmap 在 macOS 会预分配 block**，
不像 Linux 那样严格的"写时分配"。

但纯 `Truncate` 不 mmap 的话 APFS 仍是稀疏的：
```bash
dd if=/dev/zero of=/tmp/sparse bs=1 count=0 seek=1G
ls -lh /tmp/sparse    # 1.0G
du -h  /tmp/sparse    # ~0
```

→ 写 benchmark / 估磁盘容量时**别在 Mac 上推断 Linux 的行为**。

---

## 关联笔记

- [[03-paging-and-mmap]] — mmap 基础
- [[04-virtual-memory-and-mmap-internals]] — 页表 / munmap 为什么让旧地址失效
- [[unix-tools/README]] — `ls -lh` / `du -h` / `stat` 看 sparse file
