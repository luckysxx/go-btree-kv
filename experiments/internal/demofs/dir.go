// Package demofs 给文件实验分配独立目录，避免覆盖固定路径的用户文件。
package demofs

import (
	"fmt"
	"os"
)

// NewDir 创建并打印实验目录。文件保留供实验结束后检查，可手动删除该目录。
// 设置 TMPDIR 可以选择临时目录的父目录。
func NewDir(prefix string) string {
	dir, err := os.MkdirTemp("", prefix+"-")
	if err != nil {
		panic(err)
	}
	fmt.Printf("实验目录（保留供检查）：%s\n", dir)
	return dir
}
