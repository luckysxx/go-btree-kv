# 递归 Put 与路径复制

状态：已有正文 · 代码：[tree.go](../../btree/tree.go) 的 `put`、`insert`、`savePages`

## 已有零件与待写部分

页函数只做"一张页进，几张新页出"，不碰页号，也不知道自己在树中的位置：

| 已有 | 作用 |
| --- | --- |
| `leafInsertSplit(old, key, val)` | 插入或覆盖，放不下时分成 1～3 页；单条记录连空叶子都放不下时返回 ErrRecordTooLarge |
| `internalReplaceChildSplit(old, i, entries)` | 把父页第 i 个孩子换成一组目录项，放不下时分页；检查顺序与 1000 字节 key 上限 |
| `store.save(page)` | 保存页、分配页号 |

整树 Put 要补的是：把新页保存并整理成目录项、取孩子下标、串起各层的递归、检查用户 key、决定何时更新根。

## 为什么孩子不保存父页号

若孩子存父页号，父页 CoW 或分裂后，即使孩子内容不变，孩子里的父页号也要改；改了孩子又改变父目录需要保存的孩子页号，形成双向维护。这不必然导致无限循环（可以先分配页号再填写），但额外改写仍在。本实现用下降时的递归栈回到父层，不持久化父指针。

## 每层向父层交回什么

```go
func (t *tree) insert(pageID uint64, key, val []byte, visited []uint64) ([]internalEntry, error)
```

每层返回**替换这棵子树所需的目录项**：每张新页一项 `{页内第一个 key, 新页号}`，没分裂时 1 项，分裂时 2～3 项。它正好是 `internalReplaceChildSplit` 的第三个参数，父层可直接传入。

不交回字节：父层拿到字节也只是保存再取第一个 key，所以由产生新页的那一层自己保存（`savePages`）。

## 页号与下标

父层同时需要两个数：

| | 回答的问题 | 范围 | 用途 |
| --- | --- | --- | --- |
| 页号 | 页存在哪 | 页存储内全局唯一 | `store.get` 取孩子 |
| 下标 | 是父页目录的第几项 | 只在这张父页内有意义 | `internalReplaceChildSplit` 替换第几项 |

类比 C 的指针数组：`child = parent->children[i]` 顺着指针下降，回来时 `parent->children[i] = newChild` 需要槽位 i。以根页 4 `[0: ant→2, 1: dog→3]` 插入 cow 为例：路由得到下标 0 → 取页 2 → 叶子重建为页 5 → 回到根改第 0 项为 `ant→5`。

Get 只读，只要页号；Put 要改父页，两者都要，并在孩子返回前一直保留在局部变量里。所以从 `internalChild` 拆出 `internalChildIndex` 返回下标，页号由 `internalChildAt(page, i)` 读出。替换函数也可以设计成收页号、自行反查所在项，但路由时下标已经算出，反查是重复劳动。

## 路径复制

叶子即使只覆盖一个 value 没有分裂，孩子页号也变了，父页里记着的旧页号过时 → 父页重建为新页 → 祖父页也要重建……直到根：

```
旧：root(5) → 内部(3) → 叶子(1)
新：root(8) → 内部(7) → 叶子(6)    每层一张新页，一次 Put 新建"树高"张页
```

旧的 5、3、1 一字节未变，拿着旧根就能读到修改前的完整版本，这是 CoW 快照的来源。父页被"改"的方式是 `internalReplaceChildSplit`（只读 old，结果写在新字节里）加 `savePages`，而不是原地修改。

## minKey 随之更新

`savePages` 的 minKey 取新页的第一个 key，所以插入比子树原有最小值还小的 key 后，父目录里的 minKey 自动更新，不需要单独处理。

若不更新，查找其实仍然正确：`internalChild` 只把 key 分给 minKey ≤ key 的孩子，比所有目录项都小的 key 只会分给第一个孩子，而"新 key 小于子树原最小值"只可能发生在第一个孩子身上。被破坏的是不变量"目录 minKey 等于子树实际最小 key"，全树检查、删除时的边界判断都依赖它。bbolt 写回时同样把父节点的 key 更新为孩子的第一个 key。

## 失败与发布

- 能提前检查的在保存任何页之前检查：key 长度（1000 字节上限）在 `put` 入口；value 过大由叶子层 `leafInsertSplit` 报错；下降途中的缺页与损坏也发生在保存之前（保存发生在从叶子往回走的路上）。
- 有些失败只能在保存之后出现：根需要分裂（回到顶层才知道，见 D04）、磁盘满或 I/O 错误。
- 规则：`t.root` 只在全部成功后修改，且只在一处修改。失败时树仍是旧版本；已保存的新页从根不可达，成为垃圾（内存版本暂不回收）。

只改一个根指针就发布整个新版本，是 CoW 最重要的性质，见 [根分裂与发布](04-root-split-and-publication.md)。

## 常见错误

- **把页号当下标传。** `internalReplaceChildSplit(page, int(childID), …)`：下标越界时返回 ErrInvalidInternalEntries；即使不越界，换错位置后原孩子的旧项仍在，新项 minKey 不大于它，必然出现重复或逆序，会被 `internalBuildSplit` 的严格递增检查拦下。需要写 `int(childID)` 这类显式转换时，先想为什么要转：页号与下标在 Go 中都是整数，类型系统分不出来。
- **递归时传 nil 而不是 visited。** 环检测失效，遇到环会无限递归直到栈溢出；Go 的栈溢出是 fatal error，`recover` 接不住。
- **变量名遮住类型名。** 例如把孩子交回的目录项命名为 `internalEntry`，之后在同一作用域就无法再写 `[]internalEntry{…}`。

## 测试

[tree_test.go](../../btree/tree_test.go) 的 TestTreePut*：空树插入与覆盖、put 返回后调用者复用输入缓冲不影响树、不分裂时新建 2 页且旧根读出旧版本、插入最小 key 后根目录首项更新、孩子叶子分裂、提前失败不新建页不改根、下降途中缺页、环、1000 次随机插入/覆盖对照参考 map 并检查中途快照。

前置：[根定位与整树查找](02-root-and-tree-search.md)。后续：[根分裂与发布](04-root-split-and-publication.md)。页级分裂图例见 [内部页分组与 key 上限](../03-btree-pages/12-internal-grouping-and-key-limit.md)。返回 [整树组目录](README.md)。
