package btree

import (
	"encoding/binary"
	"errors"
)

// ErrRecordTooLarge 表示单条 KV 连空叶子都放不下，普通分裂无法解决。
var ErrRecordTooLarge = errors.New("btree: record does not fit in an empty leaf")

// leafRecord 是构建期间的记录视图，只借用输入字节；编码后结果页拥有独立副本。
type leafRecord struct {
	key []byte
	val []byte
}

// leafSplitPoint 为有序记录序列选择两页之间的切点。
// prefix[m] 是前 m 条记录的累计占用，包含各自的 2 字节 offset 表项，
// 不包含页头。调用者负责提供合法累计值：prefix[0]=0，严格递增，算术不溢出。
// 对已编码页，有 prefix[m] = 2*m + offset[m-1]（m>0）；更新后的序列需重新计算。
//
// 成功时左页取 [0,m)，右页取 [m,n)，两边非空且各自不超过 PageSize。
// 在所有合法切点中最小化字节占用差；差相同时选较小 m，保证结果确定。
// 不存在合法两页切法或不足两条记录时返回 (0,false)。不修改输入、不编码页。
func leafSplitPoint(prefix []int) (m int, ok bool) {
	n := len(prefix) - 1
	if n < 2 {
		return 0, false
	}
	total := prefix[n]
	bestGap := PageSize + 1
	for cut := 1; cut < n; cut++ {
		left := headerSize + prefix[cut]
		right := headerSize + total - prefix[cut]
		if left > PageSize || right > PageSize {
			continue
		}
		gap := left - right
		if gap < 0 {
			gap = -gap
		}
		if gap < bestGap {
			m, ok = cut, true
			bestGap = gap
		}
	}
	return m, ok
}

// leafInsertSplit 插入或覆盖一条 KV，返回按 key 顺序排列的一至三页。
// old 必须是合法有序叶子，输入在调用期间保持不变。失败返回 nil 和错误，旧页不变。
//
// 优先返回一页，其次尝试两页；两页无解时以本次新记录为中页，旧前后缀各占一页。
// 结果是尚未发布的内存页：最右页继承 old.sibling，其余页 sibling 暂为 0。
// 调用者分配实际页号后必须补齐结果页之间的右兄弟链接，再连接父节点；
// 本函数不分配页号，不维护已有前驱叶子的链接，不构成完整树更新或磁盘提交。
func leafInsertSplit(old, key, val []byte) ([][]byte, error) {
	// 先分别限长，避免后面的长度相加溢出；再检查完整单条记录能否放进空页。
	if len(key) > PageSize || len(val) > PageSize ||
		headerSize+2+4+len(key)+len(val) > PageSize {
		return nil, ErrRecordTooLarge
	}
	page, err := leafInsert(old, key, val)
	if err == nil {
		return [][]byte{page}, nil
	}
	if !errors.Is(err, ErrPageFull) {
		return nil, err
	}

	// 单页容纳失败：先在内存中合出更新后的记录序列，不编码超容量页。
	idx, exists := leafSeek(old, key)
	oldCount := int(nodeNKeys(old))
	newCount := oldCount + 1
	if exists {
		newCount = oldCount
	}
	records := make([]leafRecord, 0, newCount)
	for i := 0; i < idx; i++ {
		records = append(records, leafRecord{getKey(old, i), getValue(old, i)})
	}
	records = append(records, leafRecord{key, val})
	src := idx
	if exists {
		src++ // 覆盖时不保留同 key 的旧记录。
	}
	for ; src < oldCount; src++ {
		records = append(records, leafRecord{getKey(old, src), getValue(old, src)})
	}

	prefix := make([]int, len(records)+1)
	for i, record := range records {
		prefix[i+1] = prefix[i] + 2 + 4 + len(record.key) + len(record.val)
	}
	m, ok := leafSplitPoint(prefix)
	if !ok {
		// 两页无解时，idx 两侧必然非空：否则新记录和剩余旧记录就能分成两页。
		// 左右组都是合法旧页的子集，中间新记录已通过单条容量检查，三组各自可容纳。
		// records 已跳过被覆盖的旧记录；插入和覆盖时，新记录都位于 idx。
		left := leafBuild(records[:idx], 0)
		middle := leafBuild(records[idx:idx+1], 0)
		right := leafBuild(records[idx+1:], nodeSibling(old))
		return [][]byte{left, middle, right}, nil
	}

	// 两边都从自己的 idx=0 开始编码，重新生成页头、offset 和 KV 区。
	left := leafBuild(records[:m], 0)
	right := leafBuild(records[m:], nodeSibling(old))
	return [][]byte{left, right}, nil
}

// leafBuild 编码一组有序记录；调用者保证整组可容纳、长度字段可表示。
// sibling 只编码调用者给出的页号；函数本身不分配或验证页号。
func leafBuild(records []leafRecord, sibling uint64) []byte {
	page := make([]byte, PageSize)
	binary.LittleEndian.PutUint16(page[offType:], NodeLeaf)
	binary.LittleEndian.PutUint16(page[offNKeys:], uint16(len(records)))
	binary.LittleEndian.PutUint64(page[offSibling:], sibling)
	for i, record := range records {
		leafAppendKV(page, i, record.key, record.val)
	}
	return page
}
