# B+Tree 页操作函数地图

## 文件分工

文件名即职责：先看节点类型（叶子 / 内部），再看是否会产生多页（`_split`）。与节点类型无关的页格式读取放在 layout.go。整树部分按类型组织（同 bbolt 的 bucket.go）：tree 类型及其 get/put 等方法都在 tree.go；出现新概念（如 D05 的游标）再开新文件。测试文件与实现文件一一对应。

| 文件 | 内容 |
| --- | --- |
| [layout.go](layout.go) | 页格式常量、ErrPageFull、页头读取（nodeType/nodeNKeys/nodeSibling）、记录定位（kvAreaStart/kvStart/kvEnd/nodeUsedBytes）、getKey/getValue |
| [leaf.go](leaf.go) | 单张叶子页：leafGet、leafSeek、leafAppendKV、leafInsert、leafDelete（leafSeek、leafAppendKV 也被内部页复用） |
| [leaf_split.go](leaf_split.go) | 叶子一至三页重建：leafRecord、leafSplitPoint、leafInsertSplit、leafBuild |
| [internal.go](internal.go) | 单张内部页：internalEntry、internalBuild、internalChild、internalChildAt、internalChildIndex、internalRemoveChild、internalReplaceChild |
| [internal_split.go](internal_split.go) | 内部页多页：internalBuildSplit、internalReplaceChildSplit |
| [pagestore.go](pagestore.go) | 页存储接口 pageStore（save、replace、free、get）；内存页存储 memPageStore、newMemPageStore（replace 总是分配新页号，free 不做任何事）；ErrBadPageSize、ErrPageNotFound |
| [tree.go](tree.go) | 整树：tree、newTree、get、getFrom、put、insert、savePages、commitRoot、shrinkRoot、delete、remove；ErrCorruptTree |
| [check.go](check.go) | 全树检查：tree.check（同深度、引用唯一、minKey、子树范围、非根叶非空、sibling 为 0） |
| [cursor.go](cursor.go) | 游标：cursor、cursorFrame（exhausted）、newCursor、first、seek、next、valid、key、value（按路径栈遍历，不用叶子 sibling） |
| [db.go](db.go) | 数据库文件：meta（metaSlotFor、metaChecksum、encodeMeta、decodeMeta、readMetaSlot、pickMeta、bothMetasInvalid）、db、openDB、init、close、update、view、fail；页回收（rebuildFree、markReachable、minReaderTxid、releasePending）；锁（writeMu 写者锁、stateMu 状态锁、openMu 供 close 等待）；ErrDBClosed；ErrInvalidDB、ErrVersionMismatch、ErrMetaChecksum、ErrDBFailed |
| [kv.go](kv.go) | 对外接口：DB、Tx、Open、Close、Get、Put、Delete、Scan、Update、View（Get 返回副本；事务内与 Scan 回调中的切片是借用的） |
| [tx.go](tx.go) | 事务：tx、begin、release、get、put、delete、noteError、commit、rollback、dirtyIDs；事务页存储 txPageStore（save、replace、free、get；本事务新建的页复用页号，分配优先用空闲页）；ErrTxReadOnly、ErrTxClosed、ErrTxFailed |

2026-09-26 由原 node.go 拆出 layout.go 与 leaf.go，split.go 改名 leaf_split.go；只移动代码，未改逻辑。

## 概况

当前实现共 21 个页函数，另有内存页存储与整树查找（见下方两节）、leafRecord 与 internalEntry 两种临时输入类型。已支持叶子的一至三页重建，内部目录的单页/多页构建、子页选择和父目录项替换，以及从根下降的整树 get；已有整树 Put（含根分裂）、Delete（含根收缩）、游标范围扫描与全树检查 check。树经 pageStore 接口取页：单元测试用内存页存储，磁盘上经事务（db.go、tx.go）接到 Pager，提交时写页 → Sync → 写 meta（两个槽位轮流，带校验和）→ Sync。测试辅助函数不属于实现接口。

## 页头与定位

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| nodeType(page) | uint16 | 读取节点类型，叶子或内部节点 |
| nodeNKeys(page) | uint16 | 读取记录数 |
| nodeSibling(page) | uint64 | 读取右兄弟页号，0 表示没有 |
| kvAreaStart(page) | int | KV 区相对整页的起点：12 + 2*nkeys |
| kvStart(page, i) | int | 下标 i 的记录相对 KV 区的起点；首条为 0，其余取前项结束偏移 |
| kvEnd(page, i) | int | 下标 i 的记录相对 KV 区的结束位置，不含终点 |
| nodeUsedBytes(page) | int | 页头、offset 表与 KV 区总大小；空页为 12 |

记录在整页中的起点 = kvAreaStart(page) + kvStart(page, i)。

## 读取与查找

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| getKey(page, i) | []byte | 按记录下标读取 key |
| getValue(page, i) | []byte | 按记录下标读取 value |
| leafGet(page, key) | (value, found) | 按 key 二分查找；命中返回 value/true，否则 nil/false |
| leafSeek(page, key) | (idx, exists) | 找到第一个 >= key 的位置；若全都更小返回 nkeys，另告知是否存在相同 key |

getKey/getValue 和 leafGet 命中返回的切片引用原 page 的字节，没有复制数据。修改这些切片会改变底层页。

## 编码与重建

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| leafAppendKV(np, idx, key, val) | 无 | 向准备好的新页编码一条记录，并填写它的结束偏移 |
| leafInsert(old, key, val) | ([]byte, error) | 定位 key，计算新页所需空间；超页返回 nil、ErrPageFull，装得下才按前缀/新记录/后缀重建 |
| leafSplitPoint(prefix) | (m, ok) | 从合法两页切点中选字节占用差最小的位置；累计长度含 offset 表项，不含页头；由 leafInsertSplit 调用 |
| leafInsertSplit(old, key, val) | ([][]byte, error) | 优先单页，其次两页；两页无解时按旧前缀/新记录/旧后缀编码三页；单条过大返回 nil、ErrRecordTooLarge |
| leafBuild(records, sibling) | []byte | 编码一组已确认装得下的有序记录；设置最终 nkeys，从 idx=0 开始写 |

leafRecord 的 key/val 字段是构建期间借用的 []byte。编码后结果页拥有独立副本，不依赖旧页和输入缓冲的后续生命周期。

leafAppendKV 是底层编码函数，不查找位置、不自动增加 nkeys、不维护任意调用顺序下的有序性。调用前需设好新页最终 nkeys，并按 idx=0,1,2… 顺序写入，因为当前起点依赖上一项已写出的结束偏移。

leafInsert 不改变 old。exists=false 增加记录数，exists=true 替换原记录。它用 getKey/getValue 读旧页，用 leafAppendKV 写新页。

leafDelete(old, idx) 返回删去第 idx 条记录的新叶子，old 不变；删去唯一一条时返回空叶子，由调用者决定是否从父页摘掉。

## 内部节点目录

`internalEntry{minKey []byte, childPageID uint64}` 表示一个孩子子树的实际最小 key 和非零逻辑页号。每个孩子对应一条记录；内部页 value 恒为 8 字节子页号，sibling 暂为 0。

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| internalBuild(entries) | ([]byte, error) | 先验证非空、严格升序、非零页号与容量，再复制编码一个内部页；格式错误返回 ErrInvalidInternalEntries，容量不足返回 ErrPageFull |
| internalBuildSplit(entries) | ([][]byte, error) | 验证完整目录与 1000 字节 key 上限，顺序按字节分组并调整末尾单项，再调用 internalBuild；超长 key 返回 ErrKeyTooLarge |
| internalChild(page, key) | uint64 | 对合法非空内部页选择最后一个 minKey ≤ key 的孩子；没有则选择第一个孩子。只返回页号，不保证 key 存在 |
| internalChildAt(page, index) | uint64 | 读出目录第 index 项的孩子页号 |
| internalChildIndex(page, key) | int | 同上的选择规则，返回孩子在目录中的下标；internalChild 调用它。只读查找用页号，更新父页需要下标 |
| internalRemoveChild(old, childIndex) | ([]byte, error) | 删去一个孩子目录项；要求至少两个孩子，只剩一个时返回 ErrInvalidInternalEntries，由调用者把整页从上层摘掉 |
| internalReplaceChild(old, childIndex, replacements) | ([]byte, error) | 用非空新目录项替换一个已有孩子，按前缀/新项/后缀重建；超出一页返回 ErrPageFull，输入不变 |
| internalReplaceChildSplit(old, childIndex, replacements) | ([][]byte, error) | 合并替换后的完整目录，统一调用 internalBuildSplit；允许多页结果，执行整体顺序和 1000 字节 key 上限检查，输入不变 |

leafSeek、leafAppendKV 的实现只依赖共用记录布局，内部节点暂时复用它们；读取函数不验证节点类型。子页真实存在且 minKey 正确，仍由调用者保证。见 [内部节点布局与路由](../notes/03-btree-pages/10-internal-layout-and-routing.md)。

internalReplaceChild 支持一项换一项或多项，整组有序性与容量由 internalBuild 检查；非法替换下标或空列表也返回 ErrInvalidInternalEntries。它只重建一张父目录页，不执行内部节点分裂或上层递归更新。见 [父目录项替换](../notes/03-btree-pages/11-replace-child-entry.md)。

internalBuildSplit 接受合并完毕的目录，先检查整组顺序以避免跨组错误；groupEnds 保存各组右开结束下标，末组一项时把前一个分界减一。多页结果每页至少两项；单项输入仍可返回单页。结果全部 sibling=0、独立拥有字节，失败返回 nil，输入不变。maxKeySize=1000 由该入口检查，新入口 internalReplaceChildSplit 统一调用它；internalBuild、internalReplaceChild 与叶子页函数维持原有容量契约，未来整树 Put 还需检查所有用户 key。见 [内部节点分组构建](../notes/03-btree-pages/12-internal-grouping-and-key-limit.md)。

internalReplaceChildSplit 不沿用单页替换的 maxCount 限制，合并后即使只需一页也由 internalBuildSplit 校验和编码。上限检查覆盖所有保留下来的旧 key 和新 key；被替换掉的旧 key 不属于结果。返回的是新父页内容，尚无其自身页号，不执行更高父层或根更新。见 [孩子替换与父页分裂](../notes/03-btree-pages/13-child-replacement-and-parent-split.md)。

## 内存页存储

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| newMemPageStore() | *memPageStore | 空存储，下一个页号为 1 |
| save(page) | (uint64, error) | 复制整页并分配新页号；长度不是 PageSize 返回 0、ErrBadPageSize，存储与页号计数不变 |
| get(id) | ([]byte, error) | 返回借用只读视图；页号不存在（含 0）返回 nil、ErrPageNotFound |

不能覆盖已有页号，暂不回收，非并发安全。ErrPageNotFound 是页引用错误，不等于用户 key 不存在。见 [内存页存储](../notes/04-btree-tree/01-memory-page-store.md)。

## 整树查找与写入

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| newTree(store) | (*tree, error) | 保存一张空叶子页作为根；根始终存在，root 为 0 是非法状态 |
| get(key) | (value, found, error) | 从根查找；key 不存在返回 nil、false、nil，缺页或结构损坏返回 error |
| getFrom(pageID, key, visited) | (value, found, error) | 递归下降；visited 为本次查询的路径，用于发现环 |
| put(key, val) | error | 检查 key 长度 → insert → commitRoot。失败时 root 不变 |
| insert(pageID, key, val, visited) | ([]internalEntry, error) | 递归插入并路径复制，返回替换这棵子树的新目录项（1～3 项） |
| savePages(pages) | ([]internalEntry, error) | 逐页保存，minKey 取页内第一个 key |
| commitRoot(entries) | error | put 与 delete 共用的唯一发布点：0 项 → 空叶子根；多项 → 新建内部页作根（树高 +1）；1 项 → shrinkRoot 后发布 |
| shrinkRoot(rootID) | (uint64, error) | 根是只有一个孩子的内部页时逐层改用孩子作根（树高 -1），不新建页 |
| delete(key) | (found, error) | key 不存在时树不变、不新建页；存在时路径复制并经 commitRoot 发布。不做合并/借位 |
| remove(pageID, key, visited) | ([]internalEntry, changed, error) | 递归删除；未变 / 替换为 entries / 子树已空（entries 为空）三种结果 |

value 借用页内字节，调用者不能修改；页回收或接入 Pager 后需重新约定有效期。见 [根定位与整树查找](../notes/04-btree-tree/02-root-and-tree-search.md)。

## 游标（范围扫描）

| 函数 | 返回值 | 用途 |
| --- | --- | --- |
| newCursor() | *cursor | 拿住树当前的根；之后的 put 不影响它（快照） |
| first() / seek(key) | error | 定位到最小记录 / 第一条 ≥ key 的记录；没有则游标无效 |
| next() | error | 前进一条；读完一张叶子后沿路径栈换到下一张叶子 |
| valid() / key() / value() | bool / []byte | 是否停在记录上；当前记录（借用页内字节） |
| cursorFrame.exhausted() | bool | 这一层的项是否用完（叶子：记录读完；内部页：孩子走完） |

范围约定左闭右开，终点由调用者判断。出错后游标无效。见 [扫描与叶子链接](../notes/04-btree-tree/05-scan-and-leaf-links.md)。

## 约束与当前位置

- 所有函数以小写字母开头，仅供 btree 包内调用。
- helper 假设输入页格式和下标有效，没有完整损坏页校验。
- leafInsert 保持单页语义，leafInsertSplit 在两页无解时返回三页；三页上界仅适用于一张合法旧页的一次单条更新，新记录需能单独容纳。leafAppendKV / leafBuild 仍依赖调用者提供合法、可容纳的输入。
- 叶子 sibling 字段保留在页格式中，但 CoW 树不维护叶子链（更新一张叶子需复制其左侧所有叶子），当前恒为 0；扫描由游标沿路径栈完成。leafInsertSplit 的最右页继承旧 sibling 只是沿用旧约定。
- 新页是内存字节，不涉及磁盘页分配、刷盘、根切换或事务提交。

[节点定位笔记](../notes/03-btree-pages/03-node-format-and-record-addressing.md) · [leafSeek](../notes/03-btree-pages/04-leaf-search-and-insertion-position.md) · [页重建](../notes/03-btree-pages/05-leaf-rebuild-and-ownership.md) · [容量检查](../notes/03-btree-pages/06-leaf-capacity-and-errors.md) · [当前状态](../../docs/status.md)

[分裂切点选择](../notes/03-btree-pages/07-byte-based-split-point.md)

[插入到两页重建](../notes/03-btree-pages/08-leaf-insert-and-split.md)

[两页无解时的三页兜底](../notes/03-btree-pages/09-leaf-three-page-fallback.md)
