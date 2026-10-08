// Package btree 实现 B+Tree：页内节点操作、整树操作，以及接到 Pager 的单文件事务。
//
// 当前提供叶子查找、插入、最多三页重建，以及内部目录的单页/多页构建、子页选择和父目录项替换。
// internalReplaceChildSplit 已连接孩子替换与父页分裂。
// 一个节点采用 4096 字节的逻辑页，格式见 notes/03-btree-pages/03-node-format-and-record-addressing.md：
//
//	HEADER(12B): type uint16 | nkeys uint16 | sibling uint64
//	OFFSET 数组: nkeys × uint16，保存每条 KV 相对 KV 区起点的终点
//	KV 区: klen uint16 | vlen uint16 | key | val
//
// leafInsert 重建新页字节，不等同于已经具备磁盘 CoW 事务。
// leafInsertSplit 返回待发布的一至三页；非最右页的 sibling 等待分配实际页号后填写。
// internalBuildSplit 校验完整目录和 1000 字节 key 上限，按字节分组后分别构建内部页。
// internalReplaceChildSplit 合并替换后的目录，再统一调用 internalBuildSplit，返回待分配页号的新父页。
// tree 经 pageStore 接口取页，以空叶子为初始根，提供整树 get、put、delete、游标与 check；
// memPageStore 是单元测试用的内存实现，tx 是接到磁盘文件的事务实现（db.go、tx.go）。
// 学习路线与当前状态统一见工作区 docs/roadmap.md 和 docs/status.md。
package btree
