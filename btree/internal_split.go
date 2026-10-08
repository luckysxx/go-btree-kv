package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// maxKeySize 是为内存整树设计选定的 key 长度上限，单位为字节。
// 当前由 internalBuildSplit 检查，internalReplaceChildSplit 统一调用它；后续整树写入入口也必须检查所有用户 key。
// 底层 internalBuild / 叶子页函数仍按原有单页容量契约工作。
const maxKeySize = 1000

// ErrKeyTooLarge 表示 key 超过设计上限，即使它本身还能编码进一张页也不接受。
var ErrKeyTooLarge = errors.New("btree: key exceeds maximum supported size")

// internalBuildSplit 将完整的有序目录编码为按 key 顺序排列的一页或多页。
// 先验证整组非空、key 严格递增且不超过 maxKeySize、孩子页号非零，再按字节分组。
// 输入在调用期间须保持不变；真实孩子存在且 minKey 正确仍由调用者保证。
//
// 从左到右尽量装满每页；末组仅一项时，从前组移入其末项，保持跨页顺序。
// 多页结果每页至少两项；只有一项的输入仍可构建一页，不据此允许整树中的单孩子内部节点。
// 所有结果页独立拥有字节且 sibling=0，任何正常返回均不修改输入；失败返回 nil 和错误。
// 本函数不替换旧目录项、不分配页号、不更新父层或根，也不进行文件 I/O。
func internalBuildSplit(entries []internalEntry) ([][]byte, error) {
	if len(entries) == 0 {
		return nil, ErrInvalidInternalEntries
	}
	// 必须检查整组顺序：只让 internalBuild 分别检查各组，会漏掉跨组重复或逆序。
	for entryIndex, entry := range entries {
		if len(entry.minKey) > maxKeySize {
			return nil, ErrKeyTooLarge
		}
		if entry.childPageID == 0 ||
			(entryIndex > 0 && bytes.Compare(entries[entryIndex-1].minKey, entry.minKey) >= 0) {
			return nil, ErrInvalidInternalEntries
		}
	}

	// groupEnds 保存每组的右开结束下标。只记录分界，不搬动或复制输入目录项。
	var groupEnds []int
	usedBytes := headerSize
	for entryIndex, entry := range entries {
		entryBytes := 2 + 4 + len(entry.minKey) + 8
		if entryBytes > PageSize-usedBytes {
			groupEnds = append(groupEnds, entryIndex)
			usedBytes = headerSize
		}
		usedBytes += entryBytes
	}
	groupEnds = append(groupEnds, len(entries))

	lastGroupIndex := len(groupEnds) - 1
	if lastGroupIndex > 0 && groupEnds[lastGroupIndex]-groupEnds[lastGroupIndex-1] == 1 {
		// 1000 字节上限保证任意四项都能装页，因此前一个已满组至少有四项。
		// 分界左移一项后，前组至少三项，末组两项且一定可容纳。
		groupEnds[lastGroupIndex-1]--
	}

	pages := make([][]byte, 0, len(groupEnds))
	groupStart := 0
	for _, groupEnd := range groupEnds {
		page, err := internalBuild(entries[groupStart:groupEnd])
		if err != nil {
			return nil, err // 不向调用者返回已编码的部分页。
		}
		pages = append(pages, page)
		groupStart = groupEnd
	}
	return pages, nil
}

// internalReplaceChildSplit 用非空新目录项替换 old 的一个孩子，返回一页或多页新父目录。
// old 必须是合法非空内部页；输入在调用期间不变，新项须准确描述更新后的孩子。
// 非法下标或空替换返回 ErrInvalidInternalEntries；合并目录统一由 internalBuildSplit 校验，
// 包括保留的旧项与新项之间的顺序、非零孩子页号和 1000 字节 key 上限。
// 成功结果独立拥有字节且 sibling=0；失败返回 nil，任何正常返回均不修改输入。
// 本函数不分配页号、不更新更高父层或根，也不回收旧页或执行文件 I/O。
func internalReplaceChildSplit(old []byte, childIndex int, replacements []internalEntry) ([][]byte, error) {
	oldCount := int(nodeNKeys(old))
	if childIndex < 0 || childIndex >= oldCount || len(replacements) == 0 {
		return nil, ErrInvalidInternalEntries
	}
	// 允许多页结果，不沿用单页替换入口的 maxCount 限制。
	newCount := oldCount - 1 + len(replacements)
	entries := make([]internalEntry, 0, newCount)

	// A：保留被替换项之前的目录。
	for entryIndex := 0; entryIndex < childIndex; entryIndex++ {
		entries = append(entries, internalEntry{
			minKey:      getKey(old, entryIndex),
			childPageID: binary.LittleEndian.Uint64(getValue(old, entryIndex)),
		})
	}
	// B：加入新孩子目录；key 可以在调用期间借用 old 的字节。
	entries = append(entries, replacements...)
	// C：跳过旧孩子，保留其后的目录。
	for entryIndex := childIndex + 1; entryIndex < oldCount; entryIndex++ {
		entries = append(entries, internalEntry{
			minKey:      getKey(old, entryIndex),
			childPageID: binary.LittleEndian.Uint64(getValue(old, entryIndex)),
		})
	}
	// 即使结果只需一页，也走相同的顺序与 key 上限检查。
	return internalBuildSplit(entries)
}
