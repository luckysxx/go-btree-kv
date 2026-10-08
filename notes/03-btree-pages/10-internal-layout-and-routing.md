# 内部节点布局、实际最小 key 与子页路由

状态：已有正文 · 代码：[internal.go](../../btree/internal.go) 的 `internalEntry`、`internalBuild`、`internalChild`、`internalChildIndex`、`internalChildAt`

## 从范围目录到页内记录

教材常用"n 个分隔 key 对应 n+1 个孩子"来描述内部节点。本项目让**每个孩子对应一条记录**：key 是该孩子子树中实际最小的 key，value 是小端编码的 8 字节孩子页号，因此目录记录数等于孩子数。

例：目录 `ant→10、dog→25、pig→31`，三个孩子分别包含 ant/cat、dog/fox、pig/zoo。dog、pig 仍是三段范围之间的分界；额外保存最左孩子的 ant，让记录与孩子一一对应，内部页就能复用叶子的 key/value 记录格式，不必另设孩子指针数组。

内部页布局：

- 同样是 12 字节页头、每条 2 字节 offset、变长记录区。
- type = NodeInternal；nkeys = 目录记录数 = 孩子数。
- sibling 恒为 0，它不是孩子指针。
- 每条记录 = klen(2) + vlen(2) + key + childPageID(8)，vlen 恒为 8。一条 `ant→10` 在记录区占 4 + 3 + 8 = 15 字节，加上 offset 表项共 17 字节。

```text
页内范围      内容
[ 0, 2)       type = 2
[ 2, 4)       nkeys = 3
[ 4,12)       sibling = 0
[12,18)       offset[] = 15, 30, 45
[18,33)       klen=3 | vlen=8 | "ant" | 页号 10
[33,48)       klen=3 | vlen=8 | "dog" | 页号 25
[48,63)       klen=3 | vlen=8 | "pig" | 页号 31
[63,4096)     空闲
```

offset 里的 15/30/45 是页内定位信息，value 里的 10/25/31 才是孩子页号。内部页的 key 只负责路由，真正的用户 value 只在叶子里。

## 路由规则

在非空、key 严格递增的目录中，选**最后一个 minKey ≤ 查询 key** 的孩子；如果所有 minKey 都比查询 key 大，选第 0 个孩子。

| 查询 | 选中 |
| --- | --- |
| eel | dog→25 |
| dog | dog→25 |
| zoo | pig→31 |
| aardvark | ant→10（之后由叶子判断不存在） |

**最左兜底**的意义：比全树最小 key 还小的新 key 也能被路由到最左孩子并插入，插入成功后沿路径更新父目录中的最小 key。所以"到达节点的查询 key 总不小于 key[0]"不能当作无条件前提。本实现不使用特殊哨兵 key，空字节串也是合法 key。

实现上复用 `leafSeek`：它返回第一个 ≥ 查询 key 的位置，不能直接当孩子下标。精确命中就取该项；未命中且下标 > 0 时取前一项；下标为 0 时保持 0。`internalChildIndex` 返回这个下标，`internalChild` 再用 `internalChildAt` 把该项的 8 字节 value 解码成页号。只读查找用页号，更新父页时需要下标（见 [递归 Put](../04-btree-tree/03-recursive-put-and-path-copy.md#页号与下标)）。

`leafSeek`、`leafAppendKV` 的名字带 leaf，但实现只依赖共用的记录布局，内部页也复用它们。

## 构建

`internalBuild(entries)`：检查目录非空，再逐项检查 key 严格递增（`bytes.Compare >= 0` 同时拒绝重复与逆序）、页号非零、剩余容量足够；条目错误返回 `ErrInvalidInternalEntries`，超页返回 `ErrPageFull`，多种错误同时存在时不承诺固定优先级。全部通过后才分配 4096 字节、写最终条数，并用 `leafAppendKV` 编码 key 与 8 字节页号。结果复制 key，不借用输入缓冲。

构建函数不核实子树的实际内容：页号是否真实存在、minKey 是否等于孩子子树的最小 key，由调用者保证（全树检查见 [D07](../04-btree-tree/07-tree-invariants-and-model-tests.md)）。`internalChild` 要求合法的非空内部页，不做损坏页校验。

## 与教材阶数的区别

教材按固定阶数限制孩子数；本项目按实际字节容量限制目录页，一页能放多少孩子取决于 key 长度（见 [内部页分组与 key 上限](12-internal-grouping-and-key-limit.md)）。内部节点的孩子可以仍是内部节点，此时目录 key 必须是整棵孩子子树的最小 key。页号由页存储分配，内存地址不能直接持久化为页号。

## 测试

[internal_test.go](../../btree/internal_test.go)：TestInternalRouting 用内存 map 关联三个叶子，覆盖 10 种查询以及大于 uint32 的页号、页头、value 宽度与输入所有权；TestInternalBuildBoundaries 覆盖 10 种条目与容量边界。

下一篇：[单页孩子替换](11-replace-child-entry.md) · [本组目录](README.md)。
