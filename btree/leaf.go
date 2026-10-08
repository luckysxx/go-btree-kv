package btree

import (
	"bytes"
	"encoding/binary"
)

// ─── leafInsert 的"三段归并"(写 3.3 时瞄这里)────────────────────────
// 造新页 np，两个独立游标：src 走 old，dst 走 np(永远 0,1,2… = append 的 idx)。
//
//	A 前缀: copy old[0 .. idx)        → np[0 .. idx)
//	B 新条: write (key,val)           → np[idx]
//	C 后缀: copy old[src ..]          → np[idx+1 ..]
//	        src = idx   (新插, exists=false)
//	        src = idx+1 (覆盖, exists=true → 跳过老值)
//

// leafGet 在一个叶子 page 里查 key。命中 →(value,true)；没有 →(nil,false)。
// key 升序存放，故节点内二分（log₂ nkeys 次比较）。
func leafGet(page []byte, key []byte) ([]byte, bool) {
	lo, hi := 0, int(nodeNKeys(page)) // 在 [lo, hi) 上二分
	for lo < hi {
		mid := (lo + hi) / 2
		switch bytes.Compare(getKey(page, mid), key) {
		case 0: // getKey(mid) == key，命中
			return getValue(page, mid), true
		case -1: // getKey(mid) < key，往右半边找
			lo = mid + 1
		default: // getKey(mid) > key，往左半边找
			hi = mid
		}
	}
	return nil, false
}

// ─── 3.3 叶子插入（不分裂，CoW 整页重建）──────────────────────────

// leafSeek 在叶子 page 上二分，找 key 应落在的位置。
//
//	idx    = 第一个 >= key 的下标 (lower bound)；key 比所有都大时 = nkeys
//	exists = idx 处是否正好等于 key（true=覆盖更新，false=新插入）
//
// 用法：插入插在 idx 处；exists 时 idx 处是旧值，重建后缀要跳过它。
// 此定位只依赖共用的有序记录布局，internalChild 也复用它。
func leafSeek(page []byte, key []byte) (idx int, exists bool) {
	// TODO 3.3:
	//   1. lo, hi := 0, nkeys
	//   2. 二分收敛到"第一个 >= key 的下标" → idx
	//      (比 leafGet 的二分多想一步：相等时不能停，要继续往左压，才拿到最左的 >= 位置)
	//   3. exists = idx < nkeys && bytes.Equal(getKey(page, idx), key)
	lo, hi := 0, int(nodeNKeys(page))
	for lo < hi {
		mid := (lo + hi) / 2
		switch bytes.Compare(getKey(page, mid), key) {
		case -1: // getKey(mid) < key，往右半边找
			lo = mid + 1
		case 0: // getKey(mid) == key，往左半边继续找，找到最左的 >= 位置
			hi = mid
		case 1: // getKey(mid) > key，往左半边找
			hi = mid
		}
	}
	return lo, lo < int(nodeNKeys(page)) && bytes.Equal(getKey(page, lo), key)
}

// leafAppendKV 把 (key,val) 写成 np 的第 idx 条 KV。
// 此编码不依赖节点类型，internalBuild 也复用它写入目录记录。
//
// 约定：必须按 idx = 0,1,2… 顺序调用；调用前 np 的 header（尤其 nkeys）已写好，
// 否则 kvAreaStart 算错位。
//
// 自穿线：第 idx 条的相对起点 = offset[idx-1]（kvStart 帮你读，idx==0 时为 0）；
// 写完 KV 再落 offset[idx]=本条终点，下次 append 自然接上。
func leafAppendKV(np []byte, idx int, key, val []byte) {
	// TODO 3.3（对照 notes/03-btree-pages/03-node-format-and-record-addressing.md 中的 klen|vlen|key|val）：
	start := kvAreaStart(np) + kvStart(np, idx) // 绝对起点
	binary.LittleEndian.PutUint16(np[start:], uint16(len(key)))
	binary.LittleEndian.PutUint16(np[start+2:], uint16(len(val)))
	copy(np[start+4:], key)
	copy(np[start+4+len(key):], val)
	end := kvStart(np, idx) + 4 + len(key) + len(val)                 // 相对终点
	binary.LittleEndian.PutUint16(np[headerSize+idx*2:], uint16(end)) // 落 offset[idx]
}

// leafInsert 返回一个新叶子 page：在 old 上插入或覆盖 (key,val)。
//
// 要求 old 为合法有序叶子。写新页前先检查容量，超页返回 nil, ErrPageFull。
// 此处不做分裂；成功与容量不足两种结果都不修改 old。
// CoW 风格：不改 old，产新页（页重建边界见 notes/05-storage-recovery/01-in-place-update-and-cow.md）。
func leafInsert(old, key, val []byte) ([]byte, error) {
	// 单个字段已超过页长，整条记录必然放不下。
	// 先限制长度，也避免下面相加溢出 int 或编码时截断为 uint16。
	if len(key) > PageSize || len(val) > PageSize {
		return nil, ErrPageFull
	}
	idx, exists := leafSeek(old, key) // 新条落在第 idx 位；exists=该位已是同 key
	oldCount := int(nodeNKeys(old))

	need := nodeUsedBytes(old)
	newCount := oldCount
	if exists {
		// key 相同，条数及长度字段不变，只算 value 长度差。
		need += len(val) - len(getValue(old, idx))
	} else {
		// 新增：offset 表项(2) + klen/vlen(4) + key + value。
		need += 2 + 4 + len(key) + len(val)
		newCount++
	}
	if need > PageSize {
		return nil, ErrPageFull
	}

	np := make([]byte, PageSize)
	binary.LittleEndian.PutUint16(np[offType:], NodeLeaf)            // 还是叶子
	binary.LittleEndian.PutUint16(np[offNKeys:], uint16(newCount))   // 先写 nkeys，kvAreaStart 才算得对
	binary.LittleEndian.PutUint64(np[offSibling:], nodeSibling(old)) // 继承右兄弟！

	// A 前缀: old[0:idx] 原样摞到 np[0:idx]
	for i := 0; i < idx; i++ {
		leafAppendKV(np, i, getKey(old, i), getValue(old, i))
	}

	// B 新条: 摞到第 idx 位
	leafAppendKV(np, idx, key, val)

	// C 后缀: old[src:] → np[idx+1:]，两个游标错开
	src := idx
	if exists { // 覆盖：跳过 old 第 idx 条（那是被换掉的旧值）
		src++
	}
	for dst := idx + 1; src < oldCount; src, dst = src+1, dst+1 {
		leafAppendKV(np, dst, getKey(old, src), getValue(old, src))
	}

	return np, nil
}

// leafDelete 返回删去第 idx 条记录后的新叶子页，old 不变。idx 须在 [0, nkeys) 内。
// 删除只会让页变小，结果一定装得下一页；删去唯一一条记录时返回空叶子，由调用者决定如何处理。
// 新页继承 old 的 sibling（CoW 树中恒为 0）。
func leafDelete(old []byte, idx int) []byte {
	oldCount := int(nodeNKeys(old))
	newCount := oldCount - 1
	np := make([]byte, PageSize)
	binary.LittleEndian.PutUint16(np[offType:], NodeLeaf)
	binary.LittleEndian.PutUint16(np[offNKeys:], uint16(newCount)) // 先写 nkeys，kvAreaStart 才算得对
	binary.LittleEndian.PutUint64(np[offSibling:], nodeSibling(old))

	dst := 0
	for src := 0; src < oldCount; src++ {
		if src == idx {
			continue // 跳过被删除的记录
		}
		leafAppendKV(np, dst, getKey(old, src), getValue(old, src))
		dst++
	}
	return np
}
