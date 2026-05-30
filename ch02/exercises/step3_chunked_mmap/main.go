// 2.2 step3: 分段映射方案 —— 同样的 grow 场景下老 slice 仍然有效
//
// 目标：
//   实现"分段 mmap"：每 chunkSize 字节一段，文件 grow 时只追加新 chunk，
//   老 chunks 不动 → 持有指向老 chunk 的 slice 永远安全。
//
// 对比 step2：
//   - step2 同样的写法直接 SIGSEGV
//   - step3 同样的写法平稳跑完
//
// 运行：
//   go run ./ch02/exercises/step3_chunked_mmap
package main

import (
	"fmt"
	"os"
	"syscall"
)

const (
	pageSize  = 4096
	chunkSize = 16 * 1024 // 真实代码里这通常是 64MB，演示用 16KB 方便看效果
)

// 一个极简 "Pager"：用分段 mmap 管文件
type Pager struct {
	fp       *os.File
	fileSize int64    // 当前文件名义大小
	chunks   [][]byte // 每个 chunk 是一次独立的 mmap
}

func Open(path string) (*Pager, error) {
	_ = os.Remove(path)
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	p := &Pager{fp: fp}
	// 初始扩到 1 个 chunk
	if err := p.growTo(chunkSize); err != nil {
		fp.Close()
		return nil, err
	}
	return p, nil
}

// 扩文件到 newSize 字节（必须是 chunkSize 的整数倍），
// 并把映射追加到足够大。
func (p *Pager) growTo(newSize int64) error {
	if newSize <= p.fileSize {
		return nil
	}
	if err := p.fp.Truncate(newSize); err != nil { // 稀疏文件扩容，零 IO
		return err
	}
	// 追加所需的新 chunks
	for int64(len(p.chunks))*chunkSize < newSize {
		offset := int64(len(p.chunks)) * chunkSize
		ck, err := syscall.Mmap(int(p.fp.Fd()), offset, chunkSize,
			syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		p.chunks = append(p.chunks, ck)
		fmt.Printf("    [mmap] chunk #%d 地址 = %p (offset=%d, size=%d)\n",
			len(p.chunks)-1, &ck[0], offset, chunkSize)
	}
	p.fileSize = newSize
	return nil
}

// 拿第 N 页：算它落在哪个 chunk
func (p *Pager) GetPage(pageID uint64) []byte {
	byteOff := int64(pageID) * pageSize
	chunkIdx := byteOff / chunkSize
	inChunk := byteOff % chunkSize
	return p.chunks[chunkIdx][inChunk : inChunk+pageSize]
}

func (p *Pager) Close() error {
	for _, ck := range p.chunks {
		_ = syscall.Munmap(ck)
	}
	return p.fp.Close()
}

func main() {
	pg, err := Open("/tmp/chunked_demo.bin")
	if err != nil {
		panic(err)
	}
	defer pg.Close()

	// 1. 在第 1 个 chunk 里拿到 page 0
	page0 := pg.GetPage(0)
	page0[0] = 'A'
	fmt.Printf("[1] 拿到 page0，地址 = %p，写入 'A'\n", &page0[0])

	// 2. 模拟数据库长大：要用到 page 100（offset = 100*4096 = 409600 > 16KB）
	//    → 必须追加新 chunks
	if err := pg.growTo(chunkSize * 30); err != nil { // 涨到 30 个 chunk
		panic(err)
	}
	fmt.Printf("[2] 文件 grow 完，chunk 总数 = %d\n", len(pg.chunks))

	// 3. ★ 关键：老的 page0 仍然有效，因为 chunk #0 没被 munmap
	page0[1] = 'B'
	fmt.Printf("[3] 老的 page0 还能写！地址 = %p，写入 'B'\n", &page0[0])

	// 4. 用新 chunk 里的页也没问题
	page100 := pg.GetPage(100)
	page100[0] = 'Z'
	fmt.Printf("[4] 拿到 page100（在新 chunk 里），地址 = %p，写入 'Z'\n", &page100[0])

	// 5. 验证：用 hexdump 看下文件
	fmt.Println()
	fmt.Println("验证：")
	fmt.Println("  hexdump -C /tmp/chunked_demo.bin | head -2")
	fmt.Println("  → 应看到 'AB' 在 offset 0 处")
	fmt.Println("  hexdump -C -s 409600 -n 16 /tmp/chunked_demo.bin")
	fmt.Println("  → 应看到 'Z' 在 offset 409600 处")
	fmt.Println()
	fmt.Println("看一下文件名义大小 vs 真实磁盘占用 (sparse file):")
	fmt.Println("  ls -lh /tmp/chunked_demo.bin    # 名义 size")
	fmt.Println("  du -h  /tmp/chunked_demo.bin    # 真实占用")
}
