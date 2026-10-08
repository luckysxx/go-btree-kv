# Unix 文件 API：fd、打开标志与权限

状态：已有正文 · 代码：[experiments/file-api](../../experiments/file-api/)

## 要解决的问题

Go 的 `os.OpenFile`、`Read`、`Write`、`Close` 怎样对应 Unix 文件操作？后续 Pager 和持久化代码中的文件句柄、标志与权限各自负责什么？Go 的文件 API 大体是 Unix 系统调用的一层封装，但不同平台、不同 I/O 路径的对应关系要按具体接口核实。

## 文件描述符（fd）

从调用者角度，fd 是内核用来标识进程已打开资源的小整数。

```text
进程 PID=1234 的 fd 表（示意）：
┌────┬─────────────────────────┐
│ fd │ 指向哪个打开的文件       │
├────┼─────────────────────────┤
│  0 │ stdin                    │  ← 常见启动约定
│  1 │ stdout                   │
│  2 │ stderr                   │
│  3 │ /Users/luckys/db.data    │  ← open 返回的描述符
│  4 │ /tmp/db.data.tmp.918273  │
└────┴─────────────────────────┘
```

- `open()` 检查权限、建立打开状态，返回 fd。
- `read`、`write`、`close`、`fsync` 等都通过 fd 操作。
- `close()` 关闭该描述符。
- Go 的 `*os.File` 封装文件句柄，`fp.Fd()` 可取得底层句柄。

## 打开标志

访问模式与选项用位或 `|` 组合：

| Flag | 含义 |
| --- | --- |
| `O_RDONLY` | 只读 |
| `O_WRONLY` | 只写 |
| `O_RDWR` | 读写 |
| `O_CREATE` | 不存在则创建 |
| `O_EXCL` | 配合 `O_CREATE`，已存在则报错 |
| `O_TRUNC` | 打开时截断为 0 字节 |
| `O_APPEND` | 写入追加到末尾 |

```go
os.O_WRONLY | os.O_CREATE | os.O_EXCL
```

含义："写这个文件，不存在就创建，已存在就报错"。保存实验用 `O_EXCL` 防止两个调用选中同一个临时文件名而互相覆盖。注意三种访问模式（只读/只写/读写）不是各占一位的独立标志，不能随意组合。

## 文件权限 `0644`

```text
0644 = rw- r-- r--
       │   │   └── 其他人：只读
       │   └────── 同组用户：只读
       └────────── 文件所有者：读写
```

权限参数只在**创建**新文件时起作用，打开已有文件不会修改其权限。最终权限还会受 umask 等因素影响。

## 常用系统调用与 Go 入口

| 系统调用 | 作用 | Go 入口 |
| --- | --- | --- |
| `open()` | 打开或创建文件，返回 fd | `os.OpenFile` |
| `read()` | 从 fd 读字节 | `fp.Read` |
| `write()` | 向 fd 写字节 | `fp.Write` |
| `close()` | 关闭 fd | `fp.Close` |
| `fsync()` | 同步文件数据与相关元数据 | `fp.Sync()` |
| `rename()` | 重命名；同一文件系统内原子替换 | `os.Rename` |
| `unlink()` | 删除文件名 | `os.Remove` |
| `lseek()` | 调整读写位置 | `fp.Seek` |
| `mmap()` | 把文件映射进地址空间 | `syscall.Mmap` |
| `flock()` | 协作式文件锁 | `syscall.Flock` |

## 查文档

```bash
man 2 open
man 2 fsync
man 2 rename
```

`man` 第 2 章是系统调用。具体保证以当前平台的手册为准。

## 相关

[数据位置与可见性](02-data-visibility-durability.md) · [写入、同步与错误边界](04-write-sync-and-errors.md) · [原子性与并发](03-atomicity-and-concurrency.md)（`O_EXCL`、rename 和锁各自解决不同问题）· [本组目录](README.md)。
