# Unix 常用工具 / 命令速查

> 写数据库、调试存储引擎时反复用到的命令行工具。
> 每个工具一个独立 md，按"做什么 / 怎么用 / 看输出"三段记。

## 索引

- [hexdump](hexdump.md) — 以十六进制 + ASCII 查看任意文件，调试 page 布局必备
- [cat](cat.md) — 按 ASCII 直接显示文件内容（只适合纯文本）
- [man](man.md) — 查系统调用、命令文档；`man 2 open` 看系统调用文档
- [ls / stat](ls-stat.md) — 看文件元信息（大小、inode、权限、时间戳）
- [lsof](lsof.md) — 看哪个进程打开了哪些文件 / fd 列表
- [strace / dtruss](strace-dtruss.md) — 跟踪进程的系统调用（Linux 用 strace，macOS 用 dtruss）

> 写到哪个工具就加哪个，不预先全写。
