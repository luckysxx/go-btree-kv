# 叶子查找：命中结果与插入位置

状态：已有正文 · 代码：[leaf.go](../../btree/leaf.go) 的 `leafSeek`、`leafGet`

## 问题

`leafGet` 只回答"有没有、value 是什么"；插入还需要知道 key 不存在时应该放在哪里。`leafSeek` 因此返回 `(idx, exists)`。

## 契约

- `idx` 是第一个 **大于或等于** 目标 key 的记录下标（lower bound）；没有这样的记录时返回 `nkeys`。
- `exists` 表示该位置是否正好是相同的 key。
- `idx` 的范围是 `[0, nkeys]`，表示插入位置；访问现有记录前要确认 `idx < nkeys`。
- 空页返回 `(0, false)`。

已有 ant、cat、dog、fox：

| 目标 | idx | exists | 操作 |
| --- | --- | --- | --- |
| aaa | 0 | false | 在开头插入 |
| cow | 2 | false | 在 dog 前插入 |
| dog | 2 | true | 覆盖该 key 的 value |
| zoo | 4 | false | 在末尾追加 |

与 `leafGet` 的二分相比多了一步：遇到相等时不能停，要继续向左收缩，才能得到最左的 ≥ 位置。

## 为什么末尾返回 nkeys 而不是 -1

"查找失败"与"插入位置"是两件事：key 不存在由 `exists = false` 表达，插入位置仍然有效。返回 `-1` 表示"不在末尾之内"也能设计，但调用者得再把它转换成尾部位置。返回 `nkeys` 让 `idx` 始终表示新记录最终占据的位置，于是插入可以统一写成"复制前缀、写新记录、复制后缀"：尾插的后缀为空，头插的前缀为空，空页两侧都为空。

## 测试

[leaf_test.go](../../btree/leaf_test.go) 的 TestLeafSeek 覆盖头部、命中、中间、尾部和空页。

下一篇：[叶子重建与所有权](05-leaf-rebuild-and-ownership.md) · [本组目录](README.md)。
