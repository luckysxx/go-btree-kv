# 文件与 mmap 实验

这些程序是独立的小实验，辅助理解文件调用、内核页缓存和映射生命周期。学习笔记从 [存储笔记索引](../notes/README.md) 进入。

## 运行与文件位置

以下命令均在仓库根目录运行。每次运行会创建并打印一个独立的临时目录，运行结束后保留，供 `cat`、`hexdump` 或 `dbdump` 检查，看完可以删掉。

若希望集中管理本轮文件，可先创建临时父目录，再在运行时设置 `TMPDIR`：

```sh
experiment_root=$(mktemp -d)
TMPDIR="$experiment_root" go run ./experiments/mmap/step4_make_demo
```

此时复制程序打印的实际文件路径给检查工具即可。临时目录不是持久化承诺；本组实验默认用于本机 Unix 环境，mmap 示例使用 `syscall.Mmap`。

## 正常运行入口

| 知识点 | 命令 | 预期可观察结果 | 验证边界 |
| --- | --- | --- | --- |
| 文件描述符 | `go run ./experiments/unix` | 打印文件路径与 fd | 一次正常打开，不验证关闭错误 |
| open 标志 | `go run ./experiments/file-api/step1_open` | 新目录内创建空文件 | O_EXCL 的二次打开需针对同一路径；重复启动程序会产生新目录 |
| write 返回值 | `go run ./experiments/file-api/step2_write` | 写入 `hello, database!`，打印字节数 | 未调用 Sync；读回只说明可见 |
| 同步耗时 | `go run ./experiments/file-api/step3_fsync_timing -n 5` | 比较 write 与 write+Sync 两组时间 | 是本次串行负载的测量，不是通用 TPS 或断电验证 |
| rename 替换 | `go run ./experiments/file-api/step4_rename` | 目标从 OLD 变 NEW，临时文件名消失 | 正常名称替换；未同步父目录，未注入并发/崩溃 |
| 保存方式对比 | `go run ./experiments/atomic-save -n 5` | 运行 SaveData2/3 并比较耗时 | SaveData1 保留在源码；SaveData3 未同步父目录，不是完整持久化协议 |
| 基础 mmap | `go run ./experiments/mmap/step1_mmap_basic` | 64 字节映射内修改两个字节，正常结束 | 默认未同步；能读到修改不说明已获断电持久性 |
| 显式文件同步 | `go run ./experiments/mmap/step1_mmap_basic --sync` | 打印 Sync 返回成功 | 未同步父目录，未验证整机掉电 |
| 分段映射 | `go run ./experiments/mmap/step3_chunked_mmap` | 扩到 30 段后旧 page0 仍可访问；page100 写入 Z | 仅正常扩容，未同步；Close 后不得使用旧切片 |
| Pager 样本 | `go run ./experiments/mmap/step4_make_demo` | 用 WritePage 写 page 2～5 并 Sync，打印 dbdump 命令；page 0 未写，dbdump 报告 magic 不符但仍显示各页 | Pager 正常写入路径；不含 meta，不是 B+Tree 数据库或原子提交 |

两个耗时程序省略 `-n` 时仍执行 1000 次；`-n 0` 或负数会报参数错误。

## 明确选择才运行的失败演示

```sh
go run ./experiments/mmap/step1_mmap_basic --bounds-failure
go run ./experiments/mmap/step2_naive_remap
```

- `--bounds-failure` 在长度 64 的切片上访问 `data[5000]`，预期出现 Go `index out of range` panic 并非零退出。这验证 Go 边界检查，没有到达文件缺页或断电场景。
- `step2_naive_remap` 在 Munmap 后继续访问旧切片，是故意错误的生命周期示例，**排除普通 smoke**。可能产生进程内存故障；新映射也可能碰巧复用地址而没有崩溃，不能把偶然存活当作正确。只在独立进程中手动观察，不在正常运行检查里执行。

## dbdump 与回归检查

```sh
go run ./cmd/dbdump --bytes 64 /path/to/generated/demo.db
go run ./cmd/dbdump --page 2 --bytes 128 /path/to/generated/demo.db
go test ./cmd/dbdump ./pager -count=1
go test ./...
go vet ./...
```

`dbdump` 只读文件，选项放在文件名前。空文件或不足 4096 字节的文件会清楚报错并非零退出；至少一页但尾部不整页时会警告，只查看完整页。显示两个 meta 槽位的字段与校验和结果，选出 CURRENT（有效且 txid 较大者），并据此给数据页打上根、高水位以上、节点类型等标签；magic 不符的槽位不解释字段。它不是完整的损坏文件校验器（完整校验在 btree 的 openDB 与 check）。回归覆盖短 meta 页、只读性、页选择、十六进制输出、参数错误、不完整尾页、非本库 meta 与校验和不符时的退回；Pager 测试使用 `t.TempDir()` 隔离文件，覆盖正常重开、跨段扩容、越界读取与只读映射。

普通 smoke 应运行上表正常入口，并检查样本内容；失败演示应独立记录预期退出。单元/命令检查、正常关闭重开、进程崩溃、故障注入和整机掉电是不同验证层级，不互相替代。代码已准备与这些检查通过，不代表对应知识点已经教学或 review 完成。
