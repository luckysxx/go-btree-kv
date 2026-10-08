package btree

import (
	"encoding/binary"
	"errors"
)

// ErrPageFull 表示插入或覆盖的结果装不下一页；调用者不能使用返回的新页。
// 单条记录过大也返回此错误，并不意味着一定能靠页分裂解决。
var ErrPageFull = errors.New("btree: result does not fit in one page")

const (
	PageSize = 4096 // 一个节点 = 一个 Pager page

	NodeLeaf     = 1 // 叶子: KV 的 val = 真实数据
	NodeInternal = 2 // 内部: KV 的 val = 8B 子页号

	// 节点头部布局（共 12 字节）：type(2) + nkeys(2) + sibling(8)
	headerSize = 12 // 头部总长，KV 区从这里之后开始
	offType    = 0  // [0:2)  uint16 节点类型：NodeLeaf / NodeInternal
	offNKeys   = 2  // [2:4)  uint16 本节点 KV 条数
	offSibling = 4  // [4:12) uint64 右兄弟页号；CoW 树不维护叶子链（见 cursor.go），当前恒为 0
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

// nodeUsedBytes 返回页头、offset 表和 KV 区的总长度；要求 page 格式合法。
func nodeUsedBytes(page []byte) int {
	n := int(nodeNKeys(page))
	if n == 0 {
		return headerSize
	}
	return kvAreaStart(page) + kvEnd(page, n-1)
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
