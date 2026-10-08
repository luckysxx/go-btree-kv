# 文件与进程观察工具

状态：部分已整理

## 已有正文

- [hexdump 与文件观察](06-hexdump-and-file-inspection.md)：十六进制与 ASCII、指定字节范围、重复行折叠、Pager 文件观察。
- [man 查系统调用文档](01-unix-file-api.md#查文档)：`man 2 open`、`man 2 fsync`、`man 2 rename`。

## 大纲

| 工具 | 主要内容 |
| --- | --- |
| cat | 查看文本内容，与二进制观察的区别（未展开） |
| man | 手册章节，命令与系统调用文档的定位（未展开） |
| ls / stat | 文件大小、inode、权限和时间戳（未展开） |
| lsof | 哪些进程打开了哪些文件、fd 列表（未展开） |
| strace / dtruss | Linux / macOS 的系统调用跟踪（未展开） |

返回 [本组目录](README.md)。
