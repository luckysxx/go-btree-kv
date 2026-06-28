package btree

import (
	"bytes"
	"encoding/binary"
)

const (
	PageSize = 4096 // 一个节点 = 一个 Pager page

	NodeLeaf     = 1 // 叶子: KV 的 val = 真实数据
	NodeInternal = 2 // 内部: KV 的 val = 8B 子页号

	// 节点头部布局（共 12 字节）：type(2) + nkeys(2) + sibling(8)
	headerSize = 12 // 头部总长，KV 区从这里之后开始
	offType    = 0  // [0:2)  uint16 节点类型：NodeLeaf / NodeInternal
	offNKeys   = 2  // [2:4)  uint16 本节点 KV 条数
	offSibling = 4  // [4:12) uint64 右兄弟页号（叶子链表，范围扫描用）
)

// ─── 一页长什么样（写代码时瞄这里，别回去翻笔记）────────────────────
//
// 一个节点 = 一个 page = 4096 字节。三段：HEADER → OFFSET 数组 → KV 区。
//
//	byte 0      2       4                12        12+nkeys*2            4096
//	     ┌──────┬───────┬───────────────┬──────────┬──────────────────────┐
//	     │ type │ nkeys │   sibling     │ offset[] │  KV 区(变长,前→后堆) │ free
//	     │ u16  │  u16  │     u64       │ nkeys×u16│ klen|vlen|key|val ...│ (0)
//	     └──────┴───────┴───────────────┴──────────┴──────────────────────┘
//	     └────────── HEADER 12B ────────┘└ per-key ┘
//	                                      ↑ kvAreaStart = 12 + nkeys*2
//
// 一条 KV 的内部：
//
//	┌──────┬──────┬──────────┬──────────┐
//	│ klen │ vlen │   key    │  value   │   叶子: value = 真实数据
//	│ u16  │ u16  │  klen B  │  vlen B  │   内部: value = 8B 子页号
//	└──────┴──────┴──────────┴──────────┘
//	起点+0   +2    +4         +4+klen
//
// OFFSET 数组（间接层，定长 → 节点内可二分）：
//
//	offset[i] = 第 i 条 KV 的【终点】，相对 KV 区起点。约定 offset[-1]=0。
//	  kvStart(i) = offset[i-1]   (i==0 → 0)        ← 第 i 条从哪开始
//	  kvEnd(i)   = offset[i]                        ← 第 i 条到哪结束
//	  第 i 条长度 = kvEnd(i) - kvStart(i)
//	  节点已用   = 12 + nkeys*2 + offset[nkeys-1]   ← 判分裂看它
//
// 真实叶子 (cat→meow, dog→woof, sibling=7)，每格 1 字节(hex)：
//
//	off  │00 01│02 03│04..0B (sibling=7)     │0C 0D│0E 0F│ ← header + offset[0],offset[1]
//	     │01 00│02 00│07 00 00 00 00 00 00 00│0B 00│16 00│
//	     │type1│nkey2│   sibling = 7         │ =11 │ =22 │
//	KV区起点 = 12 + 2*2 = 16 (0x10):
//	0x10 │03 00│04 00│63 61 74│6D 65 6F 77 │03 00│04 00│64 6F 67│77 6F 6F 66│
//	     │klen3│vlen4│ "cat"  │  "meow"    │klen3│vlen4│ "dog"  │  "woof"   │
//	     └─────────── 第0条, 终点=11 ──────┘└────────── 第1条, 终点=22 ─────┘
//
// ─── leafInsert 的"三段归并"(写 3.3 时瞄这里)────────────────────────
// 造新页 np，两个独立游标：src 走 old，dst 走 np(永远 0,1,2… = append 的 idx)。
//
//	A 前缀: copy old[0 .. idx)        → np[0 .. idx)
//	B 新条: write (key,val)           → np[idx]
//	C 后缀: copy old[src ..]          → np[idx+1 ..]
//	        src = idx   (新插, exists=false)
//	        src = idx+1 (覆盖, exists=true → 跳过老值)
//
// 获取节点type,读offset 0
func nodeType(page []byte) uint16 { return binary.LittleEndian.Uint16(page[offType:]) }

// 获取节点NKeys,读offset 2
func nodeNKeys(page []byte) uint16 { return binary.LittleEndian.Uint16(page[offNKeys:]) }

// 获取节点Sibling,读offset 4
func nodeSibling(page []byte) uint64 { return binary.LittleEndian.Uint64(page[offSibling:]) }

// 读取节点中kv部分的起点
func kvAreaStart(page []byte) int { return headerSize + int(nodeNKeys(page))*2 }

// 读取kv第i项的末尾位置
func kvEnd(page []byte, i int) int { return int(binary.LittleEndian.Uint16(page[headerSize+i*2:])) }

// 读取kv第i项的起点位置（相对 KV 区起点；绝对化由调用方 + kvAreaStart）
func kvStart(page []byte, i int) int {
	if i == 0 {
		return 0
	}
	return kvEnd(page, i-1) // 第 i 条的起点 = 第 i-1 条的终点
}

// 获取kv第i项的key
func getKey(page []byte, i int) []byte {
	start := kvAreaStart(page) + kvStart(page, i)    // 第i条KV的起点
	klen := binary.LittleEndian.Uint16(page[start:]) // 头2字节=klen
	keyStart := start + 4                            // 跳过 klen(2)+vlen(2)
	return page[keyStart : keyStart+int(klen)]       // 切出 key
}

// 获取kv第i项的value
func getValue(page []byte, i int) []byte {
	start := kvAreaStart(page) + kvStart(page, i)      // 第i条KV的起点
	klen := binary.LittleEndian.Uint16(page[start:])   // 头2字节 = klen
	vlen := binary.LittleEndian.Uint16(page[start+2:]) // 接着2字节 = vlen
	valueStart := start + 4 + int(klen)                // 跳过 klen(2)+vlen(2)+key(klen)
	return page[valueStart : valueStart+int(vlen)]     // 切出 value
}

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
//
// 约定：必须按 idx = 0,1,2… 顺序调用；调用前 np 的 header（尤其 nkeys）已写好，
// 否则 kvAreaStart 算错位。
//
// 自穿线：第 idx 条的相对起点 = offset[idx-1]（kvStart 帮你读，idx==0 时为 0）；
// 写完 KV 再落 offset[idx]=本条终点，下次 append 自然接上。
func leafAppendKV(np []byte, idx int, key, val []byte) {
	// TODO 3.3（对照 notes/07-node-format.md §4 的 klen|vlen|key|val）：
	start := kvAreaStart(np) + kvStart(np, idx) // 绝对起点
	binary.LittleEndian.PutUint16(np[start:], uint16(len(key)))
	binary.LittleEndian.PutUint16(np[start+2:], uint16(len(val)))
	copy(np[start+4:], key)
	copy(np[start+4+len(key):], val)
	end := kvStart(np, idx) + 4 + len(key) + len(val) // 相对终点
	binary.LittleEndian.PutUint16(np[headerSize+idx*2:], uint16(end)) // 落 offset[idx]
}

// leafInsert 返回一个新叶子 page：在 old 上插入或覆盖 (key,val)。
//
// 不分裂——假设结果一定装得下一页（满了的处理留到 3.4）。
// CoW 风格：不改 old，产新页（= BoltDB 路线，见 notes/08-insert-page-mutation.md）。
func leafInsert(old, key, val []byte) []byte {
	idx, exists := leafSeek(old, key) // 新条落在第 idx 位；exists=该位已是同 key
	nold := int(nodeNKeys(old))

	nnew := nold
	if !exists { // 覆盖不增条数，纯插入才 +1
		nnew++
	}

	np := make([]byte, PageSize)
	binary.LittleEndian.PutUint16(np[offType:], NodeLeaf)        // 还是叶子
	binary.LittleEndian.PutUint16(np[offNKeys:], uint16(nnew))   // 先写 nkeys，kvAreaStart 才算得对
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
	for dst := idx + 1; src < nold; src, dst = src+1, dst+1 {
		leafAppendKV(np, dst, getKey(old, src), getValue(old, src))
	}

	return np
}
