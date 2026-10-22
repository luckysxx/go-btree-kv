package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/luckysxx/go-btree-kv/experiments/internal/demofs"
)

func main() {
	path := filepath.Join(demofs.NewDir("file-api"), "hello.txt")
	fp, err := os.OpenFile(path,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()

	fmt.Printf("打开成功，fd = %d\n", fp.Fd())
	fmt.Printf("文件路径 = %s\n", fp.Name())
}
