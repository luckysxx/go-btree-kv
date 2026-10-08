# 叶子容量与错误：先计算，再写新页

状态：已有正文 · 代码：[layout.go](../../btree/layout.go) 的 `nodeUsedBytes`、`ErrPageFull`；[leaf.go](../../btree/leaf.go) 的 `leafInsert`

## 问题

如果直接新建 4096 字节页并编码，结果装不下时不一定 panic：`copy` 会按目标剩余空间截断，留下被截短的数据和错误的长度描述。所以容量检查放在 `leafInsert` 内部、分配新页和编码**之前**；底层 `leafAppendKV` 只负责编码，由调用层满足它的容量前提。

## 容量公式

```text
空页 used    = 12
非空页 used  = 12 + 2*nkeys + offset[nkeys-1]
新增 need    = used + 2 + 4 + len(key) + len(value)
覆盖 need    = used + len(newValue) - len(oldValue)
need <= 4096 才能编码
```

- **新增**：多一个 2 字节 offset 表项，加上 4 字节长度字段、key 与 value。例：原页已用 50 字节，新增 eel→fish 后为 50 + 11 + 2 = 63 字节，漏掉 offset 表项就会算错。
- **覆盖**：key 相同，条数、offset 表大小、key 长度和长度字段都不变，空间变化只是 value 长度差。offset 表项的值会变，但每项仍占 2 字节。覆盖也可能超页。
- **恰好 4096 是合法的**，不需要预留结束标记。例：已用 4088，value 从 4 字节改为 12 字节，结果正好 4096。
- 空页要单独处理，否则会去读不存在的"最后一个 offset"。

## 实现要点

- `leafInsert` 返回 `([]byte, error)`。先拒绝单个字段超过 PageSize 的输入：既限制后续相加的范围、避免整数溢出，也避免长度转换成 uint16 时截断。
- 用 `leafSeek` 判断新增还是覆盖，算出 need，超过 PageSize 就返回 `nil, ErrPageFull`。
- `make([]byte, PageSize)` 和三段重建都在检查之后。

不变量：只读旧页完成计算；失败不交付半成品；成功的新页独立拥有字节。失败时不需要回滚，因为失败发生在任何编码之前，而不是改完再恢复。

## 失败例与边界

- 已用 4080 字节，新增 3 字节 key、8 字节 value：need = 4080 + 2 + 4 + 3 + 8 = 4097，必须拒绝。漏计 offset 就会错误放行。
- 已用 4088 字节，value 从 4 改到 13 字节：need 同样是 4097。不增加记录数也可能超页。
- 满页上等长覆盖或缩短 value 可以成功。

`ErrPageFull` 表示"单页装不下"，包括单条记录本身过大的情况，不代表一定能靠分裂解决。分裂由 [插入与分裂](08-leaf-insert-and-split.md) 和 [三页兜底](09-leaf-three-page-fallback.md) 处理；大 value 外置、损坏页校验不属于本函数的能力。`old` 仍须是合法的有序叶子。

## 测试

[leaf_test.go](../../btree/leaf_test.go) 的 TestLeafInsertCapacity（15 个子用例）：空页；头、中、尾新增；覆盖变长时恰好装满与超出一字节；65536 字节字段；满页等长覆盖、缩短与新增空记录。成功时检查完整内容、页大小、条数、已用大小与 sibling；失败时检查 nil 与错误；两种结果都比对旧页全字节，并验证修改新页不影响旧页。

下一篇：[按字节选择切点](07-byte-based-split-point.md) · [本组目录](README.md)。
