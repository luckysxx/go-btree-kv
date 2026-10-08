# 打开与故障恢复

状态：部分已整理 · 代码：[db.go](../../btree/db.go) 的 `openDB`、`readMetaSlot`、`pickMeta`；测试 [crash_test.go](../../btree/crash_test.go)、[db_test.go](../../btree/db_test.go)

## 打开就是恢复

CoW 加双 meta 的设计下，恢复没有重放步骤，打开文件本身就是全部的恢复：

1. 读两个 meta 槽位，分别校验（magic、version、校验和、槽位、根范围）。
2. 选有效且 txid 较大的一份（[双 meta](03-file-format-and-meta-validation.md#双-meta永远不原地覆盖唯一的入口)）。
3. 要求文件至少覆盖它的高水位。
4. 以它的根与高水位为准：根可达的页就是数据库，高水位以上的一律视为不存在。

打开失败时不修改文件，留给人工检查或工具（dbdump）查看。

## 残留与损坏

| 情形 | 来源 | 处理 |
| --- | --- | --- |
| 高水位以上有内容 | 失败或崩溃的提交写过的页 | 视为不存在，之后分配直接覆盖（[重启只相信 meta](02-tree-pager-integration.md#重启只相信-meta)） |
| 一个 meta 槽位无效 | 写 meta 途中崩溃（撕裂），或位翻转 | 用另一份；之后的提交会覆盖坏槽位 |
| 两个槽位都无效 | 严重损坏，或不是本库文件 | 拒绝打开 |
| 文件比高水位短 | 外部截短，或写入顺序错误（meta 先于数据页） | 拒绝打开 |

最后一行也是一种自检：若提交顺序写反（先写 meta 后写数据页），进程死在两者之间时，meta 中的高水位超出文件长度，打开立即失败。

## 合法的恢复结果

设崩溃前最后一次确认成功的是第 k 次提交，重开后的内容只能是第 k 次或第 k+1 次（崩溃时正在进行的那次）提交之后的状态。后者出现在新 meta 已进入页缓存（进程被杀）或已落盘、但尚未向调用者确认时。已确认的提交丢失，或出现其他状态，都是错误。验证方法见 [进程崩溃与 I/O 注入](../../../docs/notes/02-validation-observability/03-process-crash-and-io-injection.md)。

## LSM 中的对应

LSM 的打开有重放步骤：读 CURRENT 找到 MANIFEST，重放其中的版本变更得到当前 SST 集合；删除不在其中的孤儿文件（对应高水位以上的页）；再重放 WAL 重建 memtable，遇到第一条坏记录即停（对应撕裂的 meta）。合法结果的判断相同：已确认的写入必须在，正在进行的可有可无（概念见 [B+Tree 与 LSM](../06-storage-comparisons/03-btree-and-lsm.md)）。

## 大纲

- 打开时的只读检查模式：只校验、不打开写事务（未展开）。
- 数据页损坏：meta 有效而数据页位翻转时的发现与处理（未展开，见 [文件格式与元数据校验](03-file-format-and-meta-validation.md) 大纲）。

[本组目录](README.md)
