// 2.2 step2: 朴素 remap 方案，故意触发野指针
//
// 目标：
//   亲眼看到 "munmap 旧的、mmap 新的" 之后，
//   持有旧 slice 的代码会 SIGSEGV（或 SIGBUS）。
//
// 这是 BoltDB 早期真实踩过的坑（笔记 05 第 4 节）。
//
// 运行：
//   go run ./ch02/exercises/step2_naive_remap
//
// 预期：
//   程序在最后一行 page0[0] = 'X' 处崩溃，trace 里有：
//     unexpected fault address ...
//     fatal error: fault
//     [signal SIGSEGV: segmentation violation ...]
package main

import (
	"fmt"
	"os"
	"syscall"
)

const pageSize = 4096

func main() {
	path := "/tmp/grow_demo.bin"
	_ = os.Remove(path)

	// 1. 建一个 4KB 文件（1 页）
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()
	if err := fp.Truncate(pageSize); err != nil {
		panic(err)
	}

	// 2. mmap 整个文件（1 页）
	data, err := syscall.Mmap(int(fp.Fd()), 0, pageSize,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		panic(err)
	}
	fmt.Printf("[1] 第一次 mmap，地址 = %p，长度 = %d\n", &data[0], len(data))

	// 3. 拿一个指向 page 0 的 slice，放到一个"长期持有者"那里
	page0 := data[0:pageSize]
	page0[0] = 'A'
	fmt.Printf("[2] 拿到 page0，地址 = %p，写入 'A' 成功\n", &page0[0])

	// 4. 模拟"插入数据导致文件 grow"
	if err := fp.Truncate(pageSize * 4); err != nil {
		panic(err)
	}
	if err := syscall.Munmap(data); err != nil { // ← 老映射没了
		panic(err)
	}
	data, err = syscall.Mmap(int(fp.Fd()), 0, pageSize*4,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		panic(err)
	}
	fmt.Printf("[3] 文件涨到 16KB，重新 mmap，新地址 = %p，长度 = %d\n", &data[0], len(data))
	fmt.Printf("    注意：新地址通常和旧地址不同\n")

	// 5. ☠️ 用旧的 page0（指向已 munmap 的虚拟地址）
	fmt.Println("[4] 即将访问失效的 page0 …")
	page0[0] = 'X' // ← 这里崩
	fmt.Println("[5] 如果你看到这一行，说明这次操作系统恰好没让我们崩——"+
		"换台机器或换条件依然不可靠")
}
