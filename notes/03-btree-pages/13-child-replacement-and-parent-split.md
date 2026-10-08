# 孩子替换接入多页构建：父目录分裂

状态：已有正文 · 代码：[internal_split.go](../../btree/internal_split.go) 的 `internalReplaceChildSplit`

前置：[单页孩子替换](11-replace-child-entry.md) 与 [内部页分组](12-internal-grouping-and-key-limit.md)。

## 要解决的问题

一个孩子更新后可能变成多个孩子，父目录要用多项替换旧的一项，替换后父目录也可能超页。`internalReplaceChild` 只能返回单页，`internalBuildSplit` 能把完整目录构建成多页；本函数把"合并"与"分组编码"接起来。

A、B1 等代表按图中顺序排列的 1000 字节 key，页号仅示意：

```text
旧父页 20：A→10 | B→11 | C→12 | D→13
孩子 11 更新后：B1→41 | B2→42

合并目录：A→10 | B1→41 | B2→42 | C→12 | D→13
                       ↓ internalBuildSplit
新父页①：A→10 | B1→41 | B2→42
新父页②：C→12 | D→13
```

五项放不进一页，初始的"四加一"被调整为"三加二"。这里只重建父目录，保留目录项指向的孩子页号，不再复制孩子。结果页还没有自己的页号，由上层保存后再交给更高父层（见 [递归 Put](../04-btree-tree/03-recursive-put-and-path-copy.md)）。

## 实现

1. 校验 childIndex 指向已有孩子、replacements 非空，否则返回 `nil, ErrInvalidInternalEntries`。
2. 计算 `newCount = oldCount − 1 + len(replacements)`，创建临时 entries。
3. 依次加入旧前缀、新项、旧后缀；后缀从 `childIndex + 1` 开始，旧孩子恰好跳过一次。
4. 统一调用 `internalBuildSplit` 校验完整结果并构建一页或多页。

合并期间 minKey 可以借用旧页或替换输入，编码时复制进结果页。成功与正常报错都不修改输入，失败不返回部分页；结果页 sibling = 0、独立拥有字节。

## 两个容易遗漏的条件

- **不能沿用单页版本的条数上限。** 单页替换用 `maxCount` 提前拒绝"不可能装进一页"的输入；多页入口允许分页，例如 292 个两字节 key 合法地分成 255 项与 37 项两页。
- **结果能装进一页也要走 `internalBuildSplit`。** 如果先尝试单页 `internalReplaceChild`、只在 ErrPageFull 时才分裂，那么一个 1001 字节的 key 装得下一页，就会绕过 1000 字节上限。统一入口同时检查保留下来的旧 key 和新 key；被替换掉的旧 key 不在结果里，不再检查。

另外两点：

- **孩子数量不增加也可能分裂。** 四项 1000 字节 key 加一项 14 字节 key 恰好占 4096 字节；把末项 key 加长一个字节就超页，需要分组。删除时新 minKey 变长也会出现这种情况（见 [删除](../04-btree-tree/06-delete-rebalance-and-root-shrink.md)）。
- **新旧项交界的顺序**必须检查完整目录，不能只检查 replacements 内部。

孩子真实存在、minKey 与子树内容一致、子树范围合法仍由调用者保证，有序检查不等于验证了孩子内容。

## 测试

[internal_replace_split_test.go](../../btree/internal_replace_split_test.go)：成功用例覆盖头中尾替换、一换一/二/三、空 key 与二进制 key、借用旧 key、恰好满页与增长一字节触发分裂、尾组调整、三页结果、超过单页条数上限；逐条核对目录与页号、页头、容量、输入不变与结果独立，并在测试中临时构建上一层，用独立的线性规则检查精确 key、间隙与首尾路由。错误用例覆盖空替换、非法下标、零页号、新项重复或逆序、新旧交界重复或逆序、超长的新 key 与保留的旧 key，以及形成多页后才出现的错误，全部返回 nil 且输入不变。

后续：[完整 B+Tree](../04-btree-tree/README.md) · [本组目录](README.md)。
