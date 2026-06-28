// Package btree 在 Pager (ch02) 之上实现一棵磁盘 B+Tree。
//
// 一个节点 = 一个 page = 4096 字节。节点字节格式是 ch03 的契约，
// 完整定义见 notes/07-node-format.md，结构速览：
//
//	HEADER(12B): type uint16 | nkeys uint16 | sibling uint64
//	OFFSET 数组 : nkeys × uint16，offset[i] = 第 i 条 KV 的【终点】(相对 KV 区起点)
//	KV 区       : 每条 klen uint16 | vlen uint16 | key | val
//	             叶子 val = 真实数据；内部 val = 8B 子页号
//
// 设计推导见 notes/06-search-structures-theory.md。
//
// 本包只依赖 Pager 的 5 个 API（GetPage/NewPage/Flush/Open/Close），
// 看不到 mmap / chunk / fsync 这些下层细节。
//
// 实现进度（见 notes/progress.md）：
//
//	3.1 ✅ 字节布局契约（notes/07-node-format.md）
//	3.2 🔜 叶子节点 Get
//	3.3    叶子节点 Insert（不分裂）
//	3.4    节点分裂
//	3.5    内部节点 / 多层
//	3.6    Delete + 合并
//	3.7    Copy-on-Write 写路径 + meta.rootPageID
//	3.8    dbdump 解析 B+Tree 节点
package btree
