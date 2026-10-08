package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// ─── 内部页（目录页）长什么样 ──────────────────────────────────────
//
// 整张页是树中的一个内部节点；一条目录记录对应一个孩子。
// 和叶子共用页格式，区别是：key 为孩子子树的最小 key，value 为孩子页号。
// 所有整数均按小端编码。下图位置为页内字节偏移，不是文件页号。
//
//     0       2       4            12              12+2*childCount
//     ┌───────┬───────┬────────────┬───────────────┬──────────────────────┐
//     │ type  │ nkeys │ sibling    │ offset[]      │ 目录记录区 ... 空闲区   │
//     │ u16=2 │ u16   │ u64=0      │ 每条占 2 字节   │                      │
//     └───────┴───────┴────────────┴───────────────┴──────────────────────┘
//     └──────── 页头 12 字节 ──────┘                                  4096
//
// nkeys = childCount = 目录记录数 = 孩子数；内部页暂不使用 sibling。
// 页头不保存父页号或本页页号。本页在文件中的位置由页管理层决定。
// 每条目录记录的格式：
//
//     ┌────────┬────────┬────────────────────┬───────────────────┐
//     │ klen   │ vlen   │ key                │ value             │
//     │ u16    │ u16=8  │ 孩子子树最小 key     │  uint64 子页号     │
//     └────────┴────────┴────────────────────┴───────────────────┘
//     起点+0   +2       +4                   +4+klen     +4+klen+8
//
// offset[i] 保存第 i 条记录的结束位置，相对目录记录区起点，而不是页起点。
// 一条记录在记录区占 4+len(key)+8 字节，加上 offset 表项共贡献 14+len(key)。
//
// ─── 实例：ant→页10、dog→页25、pig→页31 ─────────────────────────────
//
// 下列所有范围均为十进制、左闭右开 [start,end)：
//
//     页内范围      内容
//     [ 0, 2)       type = 2（NodeInternal）
//     [ 2, 4)       nkeys = 3（三个孩子）
//     [ 4,12)       sibling = 0（内部页不使用）
//     [12,14)       offset[0] = 15
//     [14,16)       offset[1] = 30
//     [16,18)       offset[2] = 45
//     [18,33)       klen=3 | vlen=8 | "ant" | 子页号10
//     [33,48)       klen=3 | vlen=8 | "dog" | 子页号25
//     [48,63)       klen=3 | vlen=8 | "pig" | 子页号31
//     [63,4096)     空闲区域（新构建页中为零）
//
// 目录记录区起点 = 12+3*2 = 18；已用字节 = 18+45 = 63。
// 例如 dog 的页内起点 = 18+offset[0] = 33；它指向的文件页号却是 25。
//
// 第一条记录的实际字节（以下数值是十六进制）：
//     03 00 | 08 00 | 61 6e 74 | 0a 00 00 00 00 00 00 00
//     klen3   vlen8    "ant"     数字页号10，不是字符串 "10"
//
// 整张目录页在树中表示：
//                   [ ant→10 | dog→25 | pig→31 ]
//                      /         |         \
//                     ↓          ↓          ↓
//                   页10        页25        页31
// 孩子的页内容不嵌在本页中，本页只存通向它们的页号。

// ErrInvalidInternalEntries 表示目录为空、key 未严格递增、孩子页号为 0，或替换下标无效。
var ErrInvalidInternalEntries = errors.New("btree: invalid internal entries")

// internalEntry 是一条目录：孩子子树的实际最小 key，以及孩子的逻辑页号。
// minKey 在构建期间借用，internalBuild 会复制；childPageID 不是内存地址。
type internalEntry struct {
	minKey      []byte
	childPageID uint64
}

// internalBuild 将目录编码成一个独立的内部页，sibling 为 0。
// 输入须在调用期间保持不变；先检查条目和容量，失败返回 nil 和错误。
// 调用者保证页号确实指向对应孩子，minKey 确实是其子树最小 key。
// 本函数不分配页号、不访问孩子、不写文件。
func internalBuild(entries []internalEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, ErrInvalidInternalEntries
	}
	usedBytes := headerSize
	for entryIndex, entry := range entries {
		// 第一条没有前项；其余要求前一个 key < 当前 key。
		// Compare >= 0 包含相等和逆序，两种情况都会破坏严格递增。
		if entry.childPageID == 0 ||
			(entryIndex > 0 && bytes.Compare(entries[entryIndex-1].minKey, entry.minKey) >= 0) {
			return nil, ErrInvalidInternalEntries
		}
		// 每条需要 offset(2)、长度字段(4)、子页号(8)，剩下才是 key 空间。
		// 先用剩余容量比较，避免直接累加任意输入长度导致整数溢出。
		if len(entry.minKey) > PageSize-usedBytes-2-4-8 {
			return nil, ErrPageFull
		}
		usedBytes += 2 + 4 + len(entry.minKey) + 8
	}

	page := make([]byte, PageSize)
	binary.LittleEndian.PutUint16(page[offType:], NodeInternal)
	binary.LittleEndian.PutUint16(page[offNKeys:], uint16(len(entries)))
	var childBytes [8]byte
	for entryIndex, entry := range entries {
		binary.LittleEndian.PutUint64(childBytes[:], entry.childPageID)
		// 复用已有记录编码器：内部节点只是把 value 换成 8 字节子页号。
		leafAppendKV(page, entryIndex, entry.minKey, childBytes[:])
	}
	return page, nil
}

// internalChild 返回应访问的孩子页号，不表示查询 key 一定存在。
// page 必须是合法、非空的内部页，目录 key 严格递增，value 恰为 8 字节。
// 不修改 page；不验证损坏页，也不访问孩子或执行文件 I/O。
func internalChild(page, key []byte) uint64 {
	childIndex := internalChildIndex(page, key)
	return internalChildAt(page, childIndex)
}

// internalChildAt 返回目录第 index 项的孩子页号；index 须在 [0, nkeys) 内。
func internalChildAt(page []byte, index int) uint64 {
	return binary.LittleEndian.Uint64(getValue(page, index))
}

// internalChildIndex 返回应访问的孩子在目录中的下标，前提与 internalChild 相同。
// 更新路径需要下标，才能用 internalReplaceChildSplit 替换这一项。
func internalChildIndex(page, key []byte) int {
	// leafSeek 只依赖共用的有序记录布局，返回第一个 >= key 的位置。
	childIndex, exactMatch := leafSeek(page, key)
	if !exactMatch && childIndex > 0 {
		childIndex-- // 未命中取前一项；小于首 key 时保留第 0 项。
	}
	return childIndex
}

// internalRemoveChild 返回删去第 childIndex 个孩子目录项后的新内部页，old 不变。
// old 必须是合法内部页且至少有两个孩子：只剩一个孩子时删掉它会得到空内部页，
// 这种情况由调用者处理（把整页也从上层删掉），这里返回 ErrInvalidInternalEntries。
// 删除只会让页变小，结果一定装得下一页。
func internalRemoveChild(old []byte, childIndex int) ([]byte, error) {
	oldCount := int(nodeNKeys(old))
	if childIndex < 0 || childIndex >= oldCount || oldCount < 2 {
		return nil, ErrInvalidInternalEntries
	}
	entries := make([]internalEntry, 0, oldCount-1)
	for entryIndex := 0; entryIndex < oldCount; entryIndex++ {
		if entryIndex == childIndex {
			continue // 跳过被删除的孩子
		}
		entries = append(entries, internalEntry{
			minKey:      getKey(old, entryIndex),
			childPageID: internalChildAt(old, entryIndex),
		})
	}
	return internalBuild(entries)
}

// internalReplaceChild 用一条或多条新目录项替换 old 中 childIndex 对应的旧项。
// old 必须是合法非空内部页；输入在调用期间不变，新项须准确描述更新后的孩子。
// 成功返回复制记录的新内部页；任何正常返回均不修改 old 或 replacements。
// 非法下标/空替换返回 ErrInvalidInternalEntries；结果超页返回 ErrPageFull。
// 本函数只重建一页，不分裂父节点、不分配页号、不发布新根或回收旧页。
func internalReplaceChild(old []byte, childIndex int, replacements []internalEntry) ([]byte, error) {
	oldCount := int(nodeNKeys(old))
	if childIndex < 0 || childIndex >= oldCount || len(replacements) == 0 {
		return nil, ErrInvalidInternalEntries
	}
	// 即使 key 为空，每项也至少占 offset(2)+长度字段(4)+页号(8)。
	// 先限制条数，避免为必然超页的输入创建巨大的临时目录。
	maxCount := (PageSize - headerSize) / (2 + 4 + 8)
	if len(replacements) > maxCount-(oldCount-1) {
		return nil, ErrPageFull
	}
	newCount := oldCount - 1 + len(replacements)
	entries := make([]internalEntry, 0, newCount)

	// A：保留旧项之前的目录。
	for entryIndex := 0; entryIndex < childIndex; entryIndex++ {
		entries = append(entries, internalEntry{
			minKey:      getKey(old, entryIndex),
			childPageID: binary.LittleEndian.Uint64(getValue(old, entryIndex)),
		})
	}
	// B：加入更新后的孩子目录，一项或多项。
	entries = append(entries, replacements...)
	// C：跳过被替换的旧项，从它的下一项继续复制。
	for entryIndex := childIndex + 1; entryIndex < oldCount; entryIndex++ {
		entries = append(entries, internalEntry{
			minKey:      getKey(old, entryIndex),
			childPageID: binary.LittleEndian.Uint64(getValue(old, entryIndex)),
		})
	}
	// 检查合并后的整体顺序（包括新旧交界），并按实际字节容量编码。
	return internalBuild(entries)
}
