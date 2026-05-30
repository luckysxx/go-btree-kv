// 2.1: 第一次 mmap —— 让 []byte 和文件长在一起
//
// 目标：
//  1. 看到 mmap 返回一个 []byte，长度 = 文件大小
//  2. 通过这个 slice 读文件内容（零拷贝、零系统调用）
//  3. 改 slice → 文件内容跟着变（通过内核 page cache）
//  4. 验证修改持久化到磁盘需要 msync/fsync
//
// 运行：
//
//	go run ./ch02/exercises/step1_mmap_basic
//
// 期间用另一个终端跑：
//
//	hexdump -C /tmp/mmap_demo.bin
//	或 cat /tmp/mmap_demo.bin
package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	path := "/tmp/mmap_demo.bin"

	// ─── Step 1: 准备一个 64 字节的文件，前面写点已知内容 ─────────
	initial := []byte("HELLO MMAP! This is the start of the file.\n")
	buf := make([]byte, 64) // 64 字节，后面补 0
	copy(buf, initial)
	if err := os.WriteFile(path, buf, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("[1] 已写入文件 %s，大小 64 字节\n\n", path)

	// ─── Step 2: 以读写方式打开文件 ───────────────────────────
	fp, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()
	fmt.Printf("[2] 打开文件，fd = %d\n\n", fp.Fd())

	// ─── Step 3: mmap! ─────────────────────────────────────
	// 参数解释：
	//   fd:       要映射的文件描述符
	//   offset:   从文件的哪个字节开始映射（必须是 page size 倍数，通常 0）
	//   length:   映射多少字节
	//   prot:     允许的访问权限 PROT_READ / PROT_WRITE / PROT_EXEC
	//   flags:    MAP_SHARED  = 改动会反映回文件（多个进程映射同一文件时也互相可见）
	//             MAP_PRIVATE = 写时复制 (copy-on-write)，改动只在自己的内存里，不写回文件
	info, _ := fp.Stat()
	size := int(info.Size())
	data, err := syscall.Mmap(
		int(fp.Fd()), 0, size,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED,
	)
	if err != nil {
		panic(err)
	}
	defer syscall.Munmap(data) // 进程结束前解除映射

	fmt.Printf("[3] mmap 返回了一个 []byte，长度 = %d\n", len(data))
	fmt.Printf("    data[0]   = %q\n", data[0])
	fmt.Printf("    data[0:40]= %q\n\n", string(data[:40]))

	// ─── Step 4: 通过 mmap 改文件！ ─────────────────────────
	// 注意：这里没有任何 fp.Write / fp.WriteAt 调用！
	// 直接改 slice，内核就把这一页标"脏"，最终会自动刷盘。
	fmt.Println("[4] 通过 mmap 改 data[0] = 'X' 和 data[6] = 'x'")
	data[0] = 'X'
	data[6] = 'x'
	data[5000] = 0xff
	fmt.Printf("    现在 data[0:40] = %q\n\n", string(data[:40]))

	// ─── Step 5: msync —— 强制把脏页刷回磁盘 ─────────────────
	// 注意 mmap 没解决持久性（笔记 03 第 7 节）！
	// 不调 msync，断电时改动可能还在 page cache 里。
	// macOS 上 syscall 没直接的 Msync 包装，用 fp.Sync() 等效。
	// if err := fp.Sync(); err != nil {
	//	 panic(err)
	// }
	// fmt.Println("[5] fp.Sync() 把脏页刷回磁盘了，现在改动是持久的")
	fmt.Println()

	fmt.Println("现在另开终端跑：")
	fmt.Printf("  cat %s\n", path)
	fmt.Printf("  hexdump -C %s\n", path)
	fmt.Println("应该看到 'XELLOxMMAP!...'")
}
