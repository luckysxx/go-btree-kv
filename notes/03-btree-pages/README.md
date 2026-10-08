# B+Tree 页内算法

本组讲一张页之内的算法：搜索与页访问模型 → 字节布局 → 叶子查找、重建与分裂 → 内部目录路由、替换与分组。这些函数只处理页字节，不分配页号、不知道自己在树中的位置；把它们组装成整棵树的是 [完整 B+Tree](../04-btree-tree/README.md)。

| 顺序 | 笔记 | 状态 | 范围 | 代码入口 |
| --- | --- | --- | --- | --- |
| C1 | [搜索模型与索引取舍](01-search-models-and-index-tradeoffs.md) | 已有正文 | 比较下界、数组、平衡树与哈希 | 模型笔记 |
| C2 | [页访问成本与扇出](02-page-io-cost-and-tree-fanout.md) | 已有正文 | 页访问、树高与页内比较 | 模型笔记 |
| C3 | [节点格式与记录定位](03-node-format-and-record-addressing.md) | 已有正文 | 页头、offset、变长 KV、hex | [layout.go](../../btree/layout.go) |
| C4 | [叶子查找与插入位置](04-leaf-search-and-insertion-position.md) | 已有正文 | lower bound、空页与尾部 | `leafSeek`、`leafGet` |
| C5 | [叶子重建与所有权](05-leaf-rebuild-and-ownership.md) | 已有正文 | 三段重建、偏移重算、顺序编码 | `leafInsert`、`leafAppendKV` |
| C6 | [叶子容量与错误](06-leaf-capacity-and-errors.md) | 已有正文 | 先计算后编码、失败输入不变 | `leafInsert` |
| C7 | [按字节选择切点](07-byte-based-split-point.md) | 已有正文 | 合法切点、均衡、边界 | [leaf_split.go](../../btree/leaf_split.go) 的 `leafSplitPoint` |
| C8 | [插入与分裂](08-leaf-insert-and-split.md) | 已有正文 | 合并记录、构建结果页 | `leafInsertSplit`、`leafBuild` |
| C9 | [三页兜底](09-leaf-three-page-fallback.md) | 已有正文 | 两页无解与三页上界 | `leafInsertSplit` |
| C10 | [内部节点布局与路由](10-internal-layout-and-routing.md) | 已有正文 | 实际最小 key、孩子页号、最左兜底 | [internal.go](../../btree/internal.go) |
| C11 | [单页孩子替换](11-replace-child-entry.md) | 已有正文 | 合并目录、整组校验、单页容量 | `internalReplaceChild` |
| C12 | [内部页分组与 key 上限](12-internal-grouping-and-key-limit.md) | 已有正文 | 字节分组、尾组调整、1000 字节上限 | [internal_split.go](../../btree/internal_split.go) 的 `internalBuildSplit` |
| C13 | [孩子替换与父页分裂](13-child-replacement-and-parent-split.md) | 已有正文 | 替换接入多页构建 | `internalReplaceChildSplit` |

函数与测试入口见 [B+Tree 函数地图](../../btree/README.md)。

前置：[缓存与 Pager](../02-cache-pager/README.md)。后续：[完整 B+Tree](../04-btree-tree/README.md)。返回 [存储笔记索引](../README.md)。
