// step4: 用真的 Pager API 在独立临时目录写一个按页组织的文件。
// 然后可以用 dbdump 工具去看它的"模样"。
//
// Pager 只按页号读写，不理解 meta：本示例不写 page 0，dbdump 会报告 magic 不符，
// 但仍按页显示内容。带 meta 的真实数据库文件由 btree 的 db 创建。
//
// 运行：
//
//	go run ./experiments/mmap/step4_make_demo
//	go run ./cmd/dbdump <程序打印的实验目录>/demo.db
//	go run ./cmd/dbdump --bytes 128 <程序打印的实验目录>/demo.db
//	go run ./cmd/dbdump --page 2 --bytes 256 <程序打印的实验目录>/demo.db
package main

import (
	"fmt"
	"path/filepath"

	"dbstudy/experiments/internal/demofs"
	"dbstudy/pager"
)

func main() {
	path := filepath.Join(demofs.NewDir("pager-demo"), "demo.db")

	p, err := pager.Open(path)
	if err != nil {
		panic(err)
	}

	// 写 4 页（页号 2～5），每页开头放一段可识别的文字
	contents := []string{
		"FIRST PAGE: this is page 2, ready to hold key/value data\n",
		"SECOND PAGE: B+Tree internal node would live here\n",
		"THIRD PAGE: leaf node with records\n",
		"FOURTH PAGE: another leaf, placed after the third\n",
	}
	for i, c := range contents {
		id := uint64(2 + i)
		page := make([]byte, pager.PageSize)
		copy(page, c)
		if err := p.WritePage(id, page); err != nil {
			panic(err)
		}
		fmt.Printf("已写入 page %d: %q...\n", id, c[:30])
	}

	if err := p.Sync(); err != nil {
		panic(err)
	}
	if err := p.Close(); err != nil {
		panic(err)
	}

	fmt.Printf("\n文件已创建：%s（长度按 chunk 对齐）\n", path)
	fmt.Println("用 dbdump 看它的内部结构：")
	fmt.Printf("  go run ./cmd/dbdump %q\n", path)
	fmt.Printf("  go run ./cmd/dbdump --bytes 64 %q\n", path)
}
