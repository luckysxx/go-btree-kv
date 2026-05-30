// step4: 用真的 Pager API 造一个数据库文件，留在 /tmp/demo.db
// 然后可以用 dbdump 工具去看它的"模样"。
//
// 运行：
//   go run ./ch02/exercises/step4_make_demo
//   go run ./ch02/cmd/dbdump /tmp/demo.db
//   go run ./ch02/cmd/dbdump --bytes 128 /tmp/demo.db
//   go run ./ch02/cmd/dbdump --page 2 --bytes 256 /tmp/demo.db
package main

import (
	"fmt"
	"os"

	"dbstudy/ch02/pager"
)

func main() {
	const path = "/tmp/demo.db"
	_ = os.Remove(path)

	p, err := pager.Open(path)
	if err != nil {
		panic(err)
	}

	// 分配 4 页，每页写一些可识别的内容
	contents := []string{
		"FIRST PAGE: this is page 1, ready to hold key/value data\n",
		"SECOND PAGE: B+Tree internal node would live here\n",
		"THIRD PAGE: leaf node with records\n",
		"FOURTH PAGE: another leaf, maybe a sibling\n",
	}
	for _, c := range contents {
		id, page, err := p.NewPage()
		if err != nil {
			panic(err)
		}
		copy(page, []byte(c))
		fmt.Printf("已写入 page %d: %q...\n", id, c[:30])
	}

	if err := p.Flush(); err != nil {
		panic(err)
	}
	p.Close()

	fmt.Printf("\n数据库已创建：%s\n", path)
	fmt.Println("用 dbdump 看它的内部结构：")
	fmt.Println("  go run ./ch02/cmd/dbdump /tmp/demo.db")
	fmt.Println("  go run ./ch02/cmd/dbdump --bytes 64 /tmp/demo.db")
}
