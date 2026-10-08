# 一个孩子替换为多项：单页父目录重建

状态：已有正文 · 代码：[internal.go](../../btree/internal.go) 的 `internalReplaceChild`

## 记录重建

旧目录 `ant→10、dog→25、pig→31`；孩子 25 分裂后，调用者提供新目录项 `dog→40、eel→41`。替换下标 1，得到 `ant→10、dog→40、eel→41、pig→31`。

```text
前缀 old[0:childIndex]
  + replacements
  + 后缀 old[childIndex+1:oldCount]
  → internalBuild → 一张新的内部页
```

旧项总被跳过，结果条数为 `oldCount − 1 + len(replacements)`。**一项换一项也有意义**：CoW 更新后孩子页号会变，或者最左孩子的最小 key 会变。

`append(entries, replacements...)` 中的 `...` 把替换切片的每个元素分别追加进去，而不是把整个切片当成一项。临时 entries 只借用旧页与新项的 key，`internalBuild` 最终复制到新页，所以输入不变，结果也不依赖输入的生命周期。

## 不变量与失败

- `old` 必须是合法的非空内部页。
- `childIndex` 必须指向已有项（不能等于 oldCount），替换列表不能为空，否则返回 `nil, ErrInvalidInternalEntries`。删除孩子是另一个函数 `internalRemoveChild`。
- 新项页号是否有效、minKey 是否对应子树内容、子树之间范围是否重叠，由调用者保证。
- key 严格递增、页号非零、实际字节容量由 `internalBuild` 检查，包括前缀、替换项、后缀之间的交界：把 dog 换成 ant 会与前项重复，换成 dog/zoo 会越过后项 pig，都会被拒绝。
- 每项即使 key 为空也至少占 14 字节，所以先用 `(4096 − 12) / 14` 得到条数的保守上限，避免为必然超页的输入构建巨大的临时目录。这不是人为规定的阶数。
- 超页返回 `nil, ErrPageFull`；本函数不拆分父页，多页版本见 [孩子替换与父页分裂](13-child-replacement-and-parent-split.md)。
- 成功返回一张独立的内部页，sibling = 0；错误路径不修改输入。

本函数只重建目录：不分配父页或孩子的页号（页号由页存储分配，见 [内存页存储](../04-btree-tree/01-memory-page-store.md)），不写文件，不更新更高父层，不发布根。

## 测试

[internal_replace_test.go](../../btree/internal_replace_test.go)：TestInternalReplaceChild（仅改页号、头中尾一变二、空最小 key、一变三、非法下标、空替换、零页号、新项逆序、新旧交界错误；比较完整结果，用独立的线性规则校验路由，并验证输入不变、结果不借用输入）；TestInternalReplaceChildCapacity（满页借用旧 key 改页号、缩短 key、一变二恰好 4096 与超出一字节、单 key 超页、条数必然超页）。

下一篇：[内部页分组与 key 上限](12-internal-grouping-and-key-limit.md) · [本组目录](README.md)。
