# 文件、可见性与持久化

这一组从文件接口出发，区分数据的位置、可见性、原子性和持久性，再到整文件替换与字节观察。范围是本地普通文件；数据库的提交协议见后续的 [E 组](../05-storage-recovery/README.md)。

| 顺序 | 笔记 | 状态 | 主要内容 |
| --- | --- | --- | --- |
| 1 | [Unix 文件 API](01-unix-file-api.md) | 已有正文 | fd、打开标志、权限、系统调用、查文档 |
| 2 | [数据位置、可见性与持久性](02-data-visibility-durability.md) | 已有正文 | 三个维度、页缓存与进程退出、后台写回、mmap |
| 3 | [原子性与并发控制](03-atomicity-and-concurrency.md) | 已有正文 | rename 替换、lost update、多进程打开与协作式锁 |
| 4 | [写入、同步与错误边界](04-write-sync-and-errors.md) | 部分已整理 | 脏页、fsync 的作用范围、平台与父目录、失败承诺 |
| 5 | [临时文件与原子替换](05-atomic-file-replacement.md) | 部分已整理 | 临时文件 + rename、父目录同步 |
| 6 | [hexdump 与文件观察](06-hexdump-and-file-inspection.md) | 已有正文 | 逐字节查看、dbdump |
| 7 | [文件与进程观察工具](07-file-inspection-tools.md) | 部分已整理 | 其他工具大纲 |

## 代码入口

| 用途 | 入口 |
| --- | --- |
| 文件 API 小实验 | [experiments/file-api](../../experiments/file-api/) |
| 保存方式对照 | [atomic-save/main.go](../../experiments/atomic-save/main.go) 的 `SaveData1/2/3` |
| 文件页观察 | [cmd/dbdump](../../cmd/dbdump/main.go) |
| Pager | [pager/pager.go](../../pager/pager.go) |

运行方式见 [storage README](../../README.md) 与 [实验入口](../../experiments/README.md)。正常运行和性能输出都不构成断电验证。

下一组：[缓存、映射与 Pager](../02-cache-pager/README.md)。返回 [存储笔记总目录](../README.md)。
