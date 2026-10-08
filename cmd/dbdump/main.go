// dbdump: 把一个数据库文件的内部结构"翻译"出来给人看。
//
// 让"看不见的字节"变得能看见，避免写存储引擎时"明明跑通了但不知道造了啥"。
//
// 用法：
//
//	go run ./cmd/dbdump <文件>
//	go run ./cmd/dbdump --bytes 64 <文件> # 每页打印前 64 字节 hex
//	go run ./cmd/dbdump --bytes 256 <文件>  # 每页打印前 N 字节
//	go run ./cmd/dbdump --page 3 <文件>     # 只看某一页
//
// 输出示意：
//
//	File:    /tmp/pager_test.bin
//	Size:    16384 bytes (= 4 pages)
//
//	─── Page 0 (META slot 0) ────────────────────────
//	  magic 不符（0x0）：未写入或非本库文件
//
//	─── Page 1 (META slot 1) [CURRENT] ──────────────
//	  version=2 pageSize=4096 txid=1 root=2 highWater=3 checksum ok
//
//	─── Page 2 [ROOT] [LEAF n=0] ─────────────────────
//	  first 32B  00000000  01 00 00 00 ...
//
// meta 布局与 btree/db.go 一致（本工具不导入 btree 的未导出常量，这里另写一份）。
// CURRENT 是 magic 与校验和都正确的槽位中 txid 较大的一个，与 btree 打开时的选择规则相同；
// 除此之外只做显示，不做 btree 那样的完整校验（版本、槽位、根范围等）。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
)

const (
	pageSize = 4096

	// meta（页 0、页 1 两个槽位），同 btree/db.go
	metaSlots        = 2
	metaMagic        = 0x4b564442
	offMetaMagic     = 0
	offMetaVersion   = 4
	offMetaPageSize  = 8
	offMetaRoot      = 16
	offMetaHighWater = 24
	offMetaTxid      = 32
	offMetaChecksum  = 40

	// 节点头部，同 btree/layout.go
	nodeLeaf     = 1
	nodeInternal = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是命令入口，返回退出码；不修改输入文件。
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("dbdump", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bytesFlag := flags.Int("bytes", 0, "每页打印前 N 字节的 hex（0 = 不打印，仅显示元信息）")
	pageFlag := flags.Int("page", -1, "只打印某一页（默认全部）")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if flags.NArg() != 1 || *bytesFlag < 0 || *pageFlag < -1 {
		fmt.Fprintln(stderr, "用法: dbdump [--bytes N] [--page N] <文件>（bytes >= 0，page >= -1，选项放在文件名前）")
		return 2
	}
	path := flags.Arg(0)

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "打不开文件: %v\n", err)
		return 1
	}
	if len(data) == 0 {
		fmt.Fprintln(stderr, "文件为空，缺少完整的 meta 页")
		return 1
	}
	if len(data) < pageSize {
		fmt.Fprintf(stderr, "文件不足一页：只有 %d 字节，meta 页需要 %d 字节\n", len(data), pageSize)
		return 1
	}
	if len(data)%pageSize != 0 {
		fmt.Fprintf(stdout, "⚠️  警告: 文件大小 %d 不是 4096 的倍数（可能损坏）\n\n", len(data))
	}

	totalPages := len(data) / pageSize
	fmt.Fprintf(stdout, "File:  %s\n", path)
	fmt.Fprintf(stdout, "Size:  %d bytes (≈ %d pages of %d B)\n\n", len(data), totalPages, pageSize)

	// 两个 meta 槽位：逐个显示字段与校验结果，选出 CURRENT 用于给数据页打标签。
	slots := make([]metaInfo, 0, metaSlots)
	for slot := 0; slot < metaSlots && slot < totalPages; slot++ {
		slots = append(slots, parseMeta(data[slot*pageSize:(slot+1)*pageSize]))
	}
	info := pickCurrent(slots)
	for slot, m := range slots {
		current := ""
		if info.valid && m == info {
			current = "[CURRENT] "
		}
		fmt.Fprintf(stdout, "─── Page %d (META slot %d) %s───────────────────────────────\n", slot, slot, current)
		switch {
		case !m.magicOK:
			fmt.Fprintf(stdout, "  magic 不符（%#x）：未写入或非本库文件，不解释字段\n", m.magic)
		default:
			status := "checksum ok"
			if !m.valid {
				status = "checksum 不符（撕裂或损坏）"
			}
			fmt.Fprintf(stdout, "  version=%d pageSize=%d txid=%d root=%d highWater=%d %s\n",
				m.version, m.pageSize, m.txid, m.root, m.highWater, status)
		}
		if *bytesFlag > 0 {
			dumpHex(stdout, "  raw", data[slot*pageSize:slot*pageSize+min(*bytesFlag, pageSize)])
		}
		fmt.Fprintln(stdout)
	}
	if !info.valid {
		fmt.Fprintln(stdout, "没有 magic 与校验和都正确的 meta：数据页不按根与高水位打标签")
		fmt.Fprintln(stdout)
	}

	// 数据页
	start, end := metaSlots, totalPages
	if *pageFlag >= 0 {
		if *pageFlag >= totalPages {
			fmt.Fprintf(stderr, "page %d 不存在（文件只有 %d 页）\n", *pageFlag, totalPages)
			return 1
		}
		start, end = *pageFlag, *pageFlag+1
	}
	for id := start; id < end; id++ {
		if id < metaSlots {
			continue // meta 槽位已在上面显示
		}
		page := data[id*pageSize : (id+1)*pageSize]
		// 给"全零"的页做个标记，方便识别"分配了但没写入"的页
		allZero := true
		for _, b := range page {
			if b != 0 {
				allZero = false
				break
			}
		}
		fmt.Fprintf(stdout, "─── Page %d %s───────────────────────────────\n", id, tagFor(page, allZero, uint64(id), info))
		if *bytesFlag > 0 {
			dumpHex(stdout, "  ", page[:min(*bytesFlag, pageSize)])
		} else {
			dumpHex(stdout, "  first 32B", page[:32])
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

// metaInfo 是 dbdump 从一个 meta 槽位读出的字段。
type metaInfo struct {
	magic     uint32
	magicOK   bool
	valid     bool // magic 与校验和都正确
	version   uint32
	pageSize  uint32
	root      uint64
	highWater uint64
	txid      uint64
}

// parseMeta 读出一个 meta 槽位的字段，并核对 magic 与校验和（FNV-64a，覆盖 [0, offMetaChecksum)）。
func parseMeta(page []byte) metaInfo {
	m := metaInfo{magic: binary.LittleEndian.Uint32(page[offMetaMagic:])}
	m.magicOK = m.magic == metaMagic
	if !m.magicOK {
		return m
	}
	m.version = binary.LittleEndian.Uint32(page[offMetaVersion:])
	m.pageSize = binary.LittleEndian.Uint32(page[offMetaPageSize:])
	m.root = binary.LittleEndian.Uint64(page[offMetaRoot:])
	m.highWater = binary.LittleEndian.Uint64(page[offMetaHighWater:])
	m.txid = binary.LittleEndian.Uint64(page[offMetaTxid:])
	h := fnv.New64a()
	h.Write(page[:offMetaChecksum])
	m.valid = h.Sum64() == binary.LittleEndian.Uint64(page[offMetaChecksum:])
	return m
}

// pickCurrent 在有效的槽位中选 txid 较大者；都无效时返回零值（valid 为 false）。
func pickCurrent(slots []metaInfo) metaInfo {
	var current metaInfo
	for _, m := range slots {
		if m.valid && (!current.valid || m.txid > current.txid) {
			current = m
		}
	}
	return current
}

// tagFor 给数据页打标签，帮助 quick scan：根、高水位以上、节点类型与条数、全零。
func tagFor(page []byte, allZero bool, id uint64, info metaInfo) string {
	tags := ""
	if info.valid && id == info.root {
		tags += "[ROOT] "
	}
	if info.valid && id >= info.highWater {
		tags += "[ABOVE HIGH WATER] "
	}
	nkeys := binary.LittleEndian.Uint16(page[2:])
	switch binary.LittleEndian.Uint16(page[0:]) {
	case nodeLeaf:
		tags += fmt.Sprintf("[LEAF n=%d] ", nkeys)
	case nodeInternal:
		tags += fmt.Sprintf("[INTERNAL n=%d] ", nkeys)
	}
	if allZero {
		tags += "[ALL ZERO] "
	}
	return tags
}

// dumpHex 仿 hexdump -C 的两栏格式：左边 hex，右边 ASCII
func dumpHex(stdout io.Writer, prefix string, b []byte) {
	const cols = 16
	for off := 0; off < len(b); off += cols {
		end := off + cols
		if end > len(b) {
			end = len(b)
		}
		line := b[off:end]

		// hex
		hex := ""
		for i, c := range line {
			if i == 8 {
				hex += " "
			}
			hex += fmt.Sprintf("%02x ", c)
		}
		// pad
		for i := len(line); i < cols; i++ {
			if i == 8 {
				hex += " "
			}
			hex += "   "
		}

		// ASCII
		ascii := ""
		for _, c := range line {
			if c >= 32 && c < 127 {
				ascii += string(c)
			} else {
				ascii += "."
			}
		}

		fmt.Fprintf(stdout, "%s  %08x  %s |%s|\n", prefix, off, hex, ascii)
	}
}
