# 叶子插入与分裂：合并记录，再重建结果页

状态：已有正文 · 代码：[leaf_split.go](../../btree/leaf_split.go) 的 `leafRecord`、`leafInsertSplit`、`leafBuild`

## 问题

切点函数知道"在哪里分"，还需要有人提供本次插入或覆盖**之后**的记录序列，再把分组编码成真实的页字节。这一层只产出内存中的结果页，不涉及父节点、页号分配或提交。

## 数据路径

```text
leafInsertSplit(old, key, value)
  ├─ 单条记录连空页都装不下 → ErrRecordTooLarge
  ├─ leafInsert 成功 → 返回一页
  └─ leafInsert 返回 ErrPageFull
       → 合并：前缀 + 新记录 + 后缀（覆盖时跳过旧记录）
       → prefix：累计 offset 表项与记录字节
       → leafSplitPoint
           ├─ 有合法切点 → leafBuild(左组) + leafBuild(右组) → 返回两页
           └─ 两页无解   → 按前缀 / 新记录 / 后缀返回三页（见下一篇）
```

复用 [leaf.go](../../btree/leaf.go) 的 `leafInsert`、`leafSeek`、`leafAppendKV` 与 [layout.go](../../btree/layout.go) 的 `getKey` / `getValue`。

例：旧记录 a/c/d 占 2500/200/200（含表项），新增 b 占 1600。合并后是 a/b/c/d，prefix = `[0, 2500, 4100, 4300, 4500]`，m = 1：左页只有 a，右页是 b/c/d，分别 2512 与 2012 字节。

## 三层容量问题

1. 单条记录能否放进一张空页：不能就是 `ErrRecordTooLarge`，分裂也解决不了。
2. 更新后的全部记录能否放进一页：能就走单页 `leafInsert`。
3. 能否找到合法的两页切法：不能就进入三页兜底。

分别对字段限长，可以避免后续 int 相加溢出。单页入口与分裂路径里有少量重复的字段检查和查找，这是复用带来的，不是算法必需。

## 借用与所有权

- `leafRecord{key, val}` 在构建期间只借用 key/value 切片，不提前复制大段数据，也不尝试构造超容量页。
- 确定分组后，`leafBuild` 为每组分配独立的新页，先写最终 nkeys，再按下标顺序调用 `leafAppendKV`。每页的局部下标都从 0 开始，offset 各自重算。
- 返回的页复制了记录字节：页与输入、页与页之间互相独立，调用返回后可以复用输入缓冲区。
- `old` 必须是合法的有序叶子，输入在调用期间不变；成功返回 1～3 页，失败返回 nil 与错误，两种情况都不修改旧页和输入。

## 结果页的 sibling

结果页是**待发布的内存页**：最右页继承 `old` 的 sibling，其余页填 0，本函数不分配页号，也不维护已有前驱叶子的链接。在本项目的 CoW 树中叶子链不维护，所有 sibling 实际都是 0，扫描改用游标（见 [扫描与叶子链接](../04-btree-tree/05-scan-and-leaf-links.md)）。

## 测试

[leaf_split_test.go](../../btree/leaf_split_test.go) 的 TestLeafInsertSplit：空页与单页的插入覆盖、恰好装满、满页缩短、头中尾插入导致分裂、覆盖变长导致分裂、两页都恰好装满、单条超出空页、超长 key/value、需要三页的情形。结果与排序后的参考 map 比较（无丢失、无重复、覆盖不留旧值），检查页大小、类型、条数、容量与 sibling，比对旧页全字节，并验证清除输入或清除左页都不影响其他结果。TestLeafInsertSplitBorrowedInput 验证直接借用旧页 value 作为输入也能正确复制。

下一篇：[两页无解时的三页兜底](09-leaf-three-page-fallback.md) · [本组目录](README.md)。
