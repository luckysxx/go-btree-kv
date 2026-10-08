# 叶子页重建：新插入、覆盖与所有权

状态：已有正文 · 代码：[leaf.go](../../btree/leaf.go) 的 `leafInsert`、`leafAppendKV`

## 重建契约

`leafInsert(old, key, val)` 先检查结果页容量（见 [容量与错误](06-leaf-capacity-and-errors.md)），装得下才新建一张 4096 字节的页，按 key 顺序重新编码所有记录；旧页保持不变。这是内存中的整页重建，不等于磁盘上的 CoW 提交。

重建分三段：前缀、新记录、后缀。

```text
A 前缀: old[0 .. idx)        → np[0 .. idx)
B 新条: (key, val)           → np[idx]
C 后缀: old[src ..]          → np[idx+1 ..]
        src = idx     （新插入，exists = false，记录数 +1）
        src = idx + 1 （覆盖，exists = true，跳过被替换的旧记录）
```

## 覆盖时偏移要重算

cat→meow、dog→woof、fox→yip 各占 11、11、10 字节（含 4 字节长度字段），结束偏移为 `[11, 22, 32]`。dog 的 value 从 4 字节变成 20 字节后，结束偏移变为 `[11, 38, 48]`：

| 观察对象 | dog | fox |
| --- | --- | --- |
| 记录本体在 KV 区的起点 | 11 → 11 | 22 → 38 |
| offset 表项的下标 | 一直是 1 | 一直是 2 |
| 表项存储的结束偏移 | 22 → 38 | 32 → 48 |

**表项所在位置、表项存的数字、记录本体的起点是三个不同的概念。** dog 的起点由 cat 的终点决定，fox 的起点由 dog 的终点决定，所以 value 变长后不能沿用旧 offset 表，必须顺序重建并重算。覆盖不改变条数，offset 表仍占 6 字节，KV 区起点仍是 18。

## 新增记录会移动 KV 区起点

记录数从 3 增至 4，offset 表从 6 字节增至 8 字节，KV 区起点从 18 变成 20。所以新页必须**先写好最终 nkeys**，`leafAppendKV` 才能算对 KV 区起点。即使插在尾部、cat 内容不变，也不能把 cat 写在新页偏移 18：`[18, 20)` 已属于新增的 offset 表项。

## leafAppendKV 为什么必须按下标顺序写

写第 idx 条之前，前一条的结束偏移必须已经有效；函数用它定位本条，写完再填 `offset[idx]` 交给下一条。`idx = 0` 的相对起点固定为 0。

正常推演：cat→meow 占 4 + 3 + 4 = 11 字节，写完 `offset[0] = 11`；dog 从页内 18 + 11 = 29 开始。

反例：全零新页 nkeys = 3、KV 区起点 18。若先写 idx = 1 的 dog，它会把初始的 `offset[0] = 0` 当成有效起点，从页内 18 开始写；之后写 idx = 0 的 cat 也从 18 开始，覆盖 dog。新页中 offset 的初始零值并不是"尚未写入"的标记，函数无法识别。

所以 `leafAppendKV` 的前置条件是：先写最终 nkeys，再按 idx = 0, 1, 2… 依次调用。它是底层编码函数，不做顺序检查；由 `leafInsert`、`leafBuild` 这样的调用层保证。

## 所有权

`getKey` / `getValue` 返回的切片引用旧页字节。重建时把这些切片交给 `leafAppendKV` 复制进新页，所以结果页拥有独立的字节，不依赖旧页或调用者输入缓冲区此后的生命周期。

## 测试

[leaf_test.go](../../btree/leaf_test.go)：TestLeafInsert、TestLeafInsertEmpty，以及 TestLeafInsertOverwriteLen（value 变长、变短后读取所有记录）。

下一篇：[叶子容量与错误](06-leaf-capacity-and-errors.md) · [本组目录](README.md)。
