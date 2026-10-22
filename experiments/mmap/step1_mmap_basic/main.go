// 2.1: 第一次 mmap —— 让 []byte 和文件长在一起
//
// 目标：
//  1. 看到 mmap 返回一个 []byte，长度 = 文件大小
//  2. 通过这个 slice 读文件内容（无需每次显式调用 Read；首次访问可能缺页）
//  3. 改 slice → 文件内容跟着变（通过内核 page cache）
//  4. 区分未调用同步与显式请求文件同步；本实验不验证断电恢复
//
// 运行：
//
//	go run ./experiments/mmap/step1_mmap_basic
//
// 期间用另一个终端跑：
//
//	hexdump -C <程序打印的实验目录>/mmap_demo.bin
//	或 cat <程序打印的实验目录>/mmap_demo.bin
//
// 可选：--sync 调用文件 Sync；--bounds-failure 故意触发 Go slice 越界 panic。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/luckysxx/go-btree-kv/experiments/internal/demofs"
)

func main() {
	withSync := flag.Bool("sync", false, "修改映射后显式调用文件 Sync")
	boundsFailure := flag.Bool("bounds-failure", false, "故意访问 data[5000]，演示 Go slice 越界 panic")
	flag.Parse()
	path := filepath.Join(demofs.NewDir("mmap-basic"), "mmap_demo.bin")

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
	info, err := fp.Stat()
	if err != nil {
		panic(err)
	}
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
	if *boundsFailure {
		fmt.Println("[故意失败] 映射 slice 长 64，访问 data[5000]：Go 边界检查将 panic；这不是缺页或断电实验。")
		data[5000] = 0xff
	}
	fmt.Printf("    现在 data[0:40] = %q\n\n", string(data[:40]))

	// ─── Step 5: 可选的文件同步 ────────────────────────────
	// Sync 请求文件同步；具体保证依赖 OS、文件系统和设备，不能从 cat 读回推导。
	if *withSync {
		if err := fp.Sync(); err != nil {
			panic(err)
		}
		fmt.Println("[5] 文件 Sync 返回成功；未验证断电恢复，也未同步父目录。")
	} else {
		fmt.Println("[5] 未调用 Sync：其他读取者可观察到修改，不代表已取得断电持久性保证。")
	}
	fmt.Println()

	fmt.Println("现在另开终端跑：")
	fmt.Printf("  cat %s\n", path)
	fmt.Printf("  hexdump -C %s\n", path)
	fmt.Println("应该看到 'XELLO xMAP!...'")
}
