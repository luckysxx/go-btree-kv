// Step 1: 打开一个文件，看看 fd 是几
//
// 目标：
//   - 亲眼看到 fd 是个小整数（通常 = 3，前面 0/1/2 被 stdin/stdout/stderr 占着）
//   - 理解 O_CREATE / O_EXCL / O_TRUNC 这些 flag 的实际行为
//
// 运行：
//
//	go run ./experiments/file-api/step1_open
//
// 实验：
//  1. 去掉 O_CREATE，文件不存在 → 报错 "no such file or directory" (ENOENT)
//  2. 把 O_TRUNC 换成 O_EXCL，在同一路径调用两次 OpenFile → 第二次报错 "file exists" (EEXIST)
//     这就是 O_EXCL 的"防呆"作用：文件已存在就报错，绝不偷偷覆盖。
//     O_EXCL 本身 *不会* 自动改名，自动选择临时名字是应用层另做的事；本例每次运行默认创建新实验目录。
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

	fmt.Printf("打开成功，fd = %d\n", fp.Fd())
	fmt.Printf("文件路径 = %s\n", fp.Name())
}
