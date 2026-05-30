// Step 4: rename 的原子切换
//
// 目标：
//   亲眼看到 rename 是"改一个目录项指针"，O(1) 瞬间完成，且对其他读者原子。
//
// 运行：
//   go run ./exercises/step4_rename
//
// 预期输出：
//   rename 前：
//     /tmp/db.data     : "OLD CONTENT\n"
//     /tmp/db.data.tmp : "NEW CONTENT\n"
//   rename 后：
//     /tmp/db.data     : "NEW CONTENT\n"
//     /tmp/db.data.tmp : <不存在>
//
// inode 视角发生了什么：
//   rename 前:
//     目录项 "db.data"     → inode #100 (内容 "OLD CONTENT")
//     目录项 "db.data.tmp" → inode #101 (内容 "NEW CONTENT")
//   rename 后 (一个原子步骤):
//     目录项 "db.data"     → inode #101 (内容 "NEW CONTENT")
//     目录项 "db.data.tmp" → 不存在
//     inode #100 没人引用了 → 被回收
//
// "原子"的精确含义:
//   内核保证任何时刻 open("db.data") 要么看到旧 inode，要么看到新 inode，
//   不存在 "两者都看不到" 或 "看到半成品" 的瞬间。
//   这一性质由文件系统的元数据日志 (journal) 保证。
//
// 跨平台坑:
//   Unix 的 rename 是覆盖式原子的。
//   Windows 的 rename 不能覆盖已存在文件，必须先删 → 跨平台代码更复杂。
package main

import (
	"fmt"
	"os"
)

func main() {
	// 1. 先建一个"老"文件
	os.WriteFile("/tmp/db.data", []byte("OLD CONTENT\n"), 0644)

	// 2. 写一个"新"文件到临时位置
	fp, _ := os.OpenFile("/tmp/db.data.tmp",
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	fp.Write([]byte("NEW CONTENT\n"))
	fp.Sync() // 先确保新数据落盘
	fp.Close()

	fmt.Println("rename 前：")
	dump("/tmp/db.data")
	dump("/tmp/db.data.tmp")

	// 3. 原子切换
	if err := os.Rename("/tmp/db.data.tmp", "/tmp/db.data"); err != nil {
		panic(err)
	}

	fmt.Println("rename 后：")
	dump("/tmp/db.data")
	dump("/tmp/db.data.tmp")
}

func dump(path string) {
	if data, err := os.ReadFile(path); err != nil {
		fmt.Printf("  %s : <不存在>\n", path)
	} else {
		fmt.Printf("  %s : %q\n", path, string(data))
	}
}
