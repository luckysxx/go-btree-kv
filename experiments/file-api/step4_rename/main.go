// Step 4: rename 的原子切换
//
// 目标：
//
//	观察同一目录内 rename 前后，文件名引用的内容发生切换。
//
// 运行：
//
//	go run ./experiments/file-api/step4_rename
//
// 预期输出：
//
//	rename 前：
//	  <实验目录>/db.data     : "OLD CONTENT\n"
//	  <实验目录>/db.data.tmp : "NEW CONTENT\n"
//	rename 后：
//	  <实验目录>/db.data     : "NEW CONTENT\n"
//	  <实验目录>/db.data.tmp : <不存在>
//
// inode 视角发生了什么：
//
//	rename 前:
//	  目录项 "db.data"     → inode #100 (内容 "OLD CONTENT")
//	  目录项 "db.data.tmp" → inode #101 (内容 "NEW CONTENT")
//	rename 后 (一个原子步骤):
//	  目录项 "db.data"     → inode #101 (内容 "NEW CONTENT")
//	  目录项 "db.data.tmp" → 不存在
//	  inode #100 在链接和打开引用都释放后才可回收
//
// "原子"的精确含义:
//
//	内核保证任何时刻 open("db.data") 要么看到旧 inode，要么看到新 inode，
//	不存在 "两者都看不到" 或 "看到半成品" 的瞬间。
//	这里讨论本地 Unix 环境中同一目录内的名称替换，不由此推导断电恢复结果。
//
// 平台边界:
//
//	本实验未验证跨文件系统或其他平台的替换行为；移植时应核对平台 API 契约。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"dbstudy/experiments/internal/demofs"
)

func main() {
	dir := demofs.NewDir("rename")
	path, tmp := filepath.Join(dir, "db.data"), filepath.Join(dir, "db.data.tmp")
	// 1. 先建一个老文件；所有文件都在本次新建的实验目录中。
	if err := os.WriteFile(path, []byte("OLD CONTENT\n"), 0644); err != nil {
		panic(err)
	}
	// 2. 写新文件，显式请求文件同步。
	fp, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()
	if _, err := fp.Write([]byte("NEW CONTENT\n")); err != nil {
		panic(err)
	}
	if err := fp.Sync(); err != nil {
		panic(err)
	}
	if err := fp.Close(); err != nil {
		panic(err)
	}
	fmt.Println("rename 前：")
	dump(path)
	dump(tmp)
	// 3. 原子切换文件名；这里没有父目录同步或断电注入。
	if err := os.Rename(tmp, path); err != nil {
		panic(err)
	}
	fmt.Println("rename 后：")
	dump(path)
	dump(tmp)
	fmt.Println("本实验观察正常运行时文件名的切换；未验证断电恢复，未同步父目录。")
}

func dump(path string) {
	if data, err := os.ReadFile(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("  %s : <不存在>\n", path)
		} else {
			fmt.Fprintf(os.Stderr, "  %s : <读取失败: %v>\n", path, err)
		}
	} else {
		fmt.Printf("  %s : %q\n", path, string(data))
	}
}
