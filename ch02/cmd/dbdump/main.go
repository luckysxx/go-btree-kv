// dbdump: 把一个 Pager 文件的内部结构"翻译"出来给人看。
//
// 让"看不见的字节"变得能看见，避免写存储引擎时"明明跑通了但不知道造了啥"。
//
// 用法：
//   go run ./ch02/cmd/dbdump <文件>
//   go run ./ch02/cmd/dbdump <文件> --bytes      # 每页打印前 64 字节 hex
//   go run ./ch02/cmd/dbdump <文件> --bytes 256  # 每页打印前 N 字节
//   go run ./ch02/cmd/dbdump <文件> --page 3     # 只看某一页
//
// 输出示意：
//   File:    /tmp/pager_test.bin
//   Size:    16384 bytes (= 4 pages)
//
//   ─── Page 0 (META) ───────────────────────────────
//   nPages       = 4
//   (剩余字段保留)
//
//   ─── Page 1 ──────────────────────────────────────
//   first bytes  : 68 65 6c 6c 6f 20 66 72 6f 6d 20 70 61 67 65 21  |hello from page!|
//                  00 00 00 00 00 00 ...
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
)

const (
	pageSize  = 4096
	offNPages = 0
)

func main() {
	bytesFlag := flag.Int("bytes", 0, "每页打印前 N 字节的 hex（0 = 不打印，仅显示元信息）")
	pageFlag := flag.Int("page", -1, "只打印某一页（默认全部）")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: dbdump <文件> [--bytes N] [--page N]")
		os.Exit(2)
	}
	path := flag.Arg(0)

	data, err := os.ReadFile(path)
	if err != nil {
		fail("打不开文件: %v", err)
	}
	if len(data) == 0 {
		fail("文件为空")
	}
	if len(data)%pageSize != 0 {
		fmt.Printf("⚠️  警告: 文件大小 %d 不是 4096 的倍数（可能损坏）\n\n", len(data))
	}

	totalPages := len(data) / pageSize
	fmt.Printf("File:  %s\n", path)
	fmt.Printf("Size:  %d bytes (≈ %d pages of %d B)\n\n", len(data), totalPages, pageSize)

	// Meta page
	meta := data[0:pageSize]
	nPages := binary.LittleEndian.Uint64(meta[offNPages:])
	fmt.Println("─── Page 0 (META) ───────────────────────────────")
	fmt.Printf("  nPages (offset %d, 8 bytes LE) = %d\n", offNPages, nPages)
	fmt.Printf("  (后续 ch03 会在 offset 8 加 rootPageID 等字段)\n")
	if *bytesFlag > 0 {
		dumpHex("  raw", meta[:min(*bytesFlag, pageSize)])
	}
	fmt.Println()

	// 数据页
	start, end := 1, totalPages
	if *pageFlag >= 0 {
		if *pageFlag >= totalPages {
			fail("page %d 不存在（文件只有 %d 页）", *pageFlag, totalPages)
		}
		start, end = *pageFlag, *pageFlag+1
	}
	for id := start; id < end; id++ {
		if id == 0 {
			continue // 已在上面处理
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
		fmt.Printf("─── Page %d %s───────────────────────────────\n", id, tagFor(allZero, id, int(nPages)))
		if *bytesFlag > 0 {
			dumpHex("  ", page[:min(*bytesFlag, pageSize)])
		} else {
			dumpHex("  first 32B", page[:32])
		}
		fmt.Println()
	}
}

// tagFor 给某一页打标签，帮助 quick scan
func tagFor(allZero bool, id, nPages int) string {
	tags := ""
	if id >= nPages {
		tags += "[UNALLOCATED] "
	}
	if allZero {
		tags += "[ALL ZERO] "
	}
	return tags
}

// dumpHex 仿 hexdump -C 的两栏格式：左边 hex，右边 ASCII
func dumpHex(prefix string, b []byte) {
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

		fmt.Printf("%s  %08x  %s |%s|\n", prefix, off, hex, ascii)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
