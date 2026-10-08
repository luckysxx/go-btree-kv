// Step 2: 写入数据，观察 write 的返回值（不 fsync）
//
// 目标：
//   - 看到 write 写了多少字节（Go 的 fp.Write 已经帮你做了循环写完整）
//   - 理解 "cat 能看到" ≠ "数据落盘"：
//     另一个进程读时，内核会从 page cache 直接返回。
//     从应用层根本看不出来数据有没有真的写到磁盘。
//     只有断电那一刻才暴露问题。
//
// 运行：
//
//	go run ./experiments/file-api/step2_write
//	cat <程序打印的实验目录>/hello.txt
//
// 关键认知：
//
//	POSIX 的 write(2) 不保证一次写完，写大块/管道/socket 时可能短写。
//	Go 标准库的 fp.Write 内部循环到写完，所以你看到 n == len(data)。
//	但 syscall.Write 这种低级调用 *不保证*，要自己循环。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"dbstudy/experiments/internal/demofs"
)

func main() {
	path := filepath.Join(demofs.NewDir("file-api"), "hello.txt")
	fp, err := os.OpenFile(path,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()

	data := []byte("hello, database!\n")
	n, err := fp.Write(data)
	if err != nil {
		panic(err)
	}
	fmt.Printf("文件：%s\n写入了 %d 字节，data 长度 %d\n", path, n, len(data))
	fmt.Println("函数返回了，但数据可能还在 page cache 里")
}
