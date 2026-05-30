// Package pager 提供"按页随机存取"的能力。
//
// 设计 (见 notes/00-roadmap.md)：
//   - 文件按 4KB 分页
//   - 用分段 mmap (chunk) 避免 grow 时野指针
//   - page 0 = meta page，存全局少数几个事实（目前只有 nPages）
//
// 对外 API：
//
//	Open(path) → *Pager
//	GetPage(id) → []byte (4096 字节)
//	NewPage()  → (id, []byte)
//	Flush()    → 写回 meta + fsync
//	Close()
//
// 上层（B+Tree）只看到这 5 个函数，看不到 mmap / chunk / fsync 这些细节。
package pager

import (
	"encoding/binary"
	"os"
	"syscall"
)

const (
	PageSize  = 4096
	ChunkSize = 16 * 1024 // 演示值；生产代码用 64MB
	MetaPage  = 0         // page 0 永远是 meta page
)

// Meta page 字段在 page 0 中的 byte 偏移
const (
	offNPages = 0 // uint64, 8 字节: 已分配的页数
	// 后续 ch03 会在这里加 rootPageID, ch04 加 freeListHead 等
)

type Pager struct {
	fp       *os.File
	fileSize int64    // 当前文件名义大小（= 已 mmap 的总字节数）
	chunks   [][]byte // 每个 chunk 一次独立 mmap
	nPages   uint64   // 已分配的页数（启动时从 meta 读，Flush 时写回 meta）
}

// ─── Open / Close ──────────────────────────────────────────────

// Open 打开或创建一个数据库文件。
// 新文件：初始化 1 页（meta page），nPages = 1。
// 已存在文件：从 page 0 读出 nPages。
func Open(path string) (*Pager, error) {
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}

	p := &Pager{fp: fp}

	info, err := fp.Stat()
	if err != nil {
		fp.Close()
		return nil, err
	}

	if info.Size() == 0 {
		// === 新数据库 ===
		// 1. 扩到 1 个 chunk 大小，mmap 出来
		if err := p.growMmap(ChunkSize); err != nil {
			fp.Close()
			return nil, err
		}
		// 2. nPages = 1 (page 0 自己就占一页)
		p.nPages = 1
		// 3. 立即把 meta 写一份到 page 0，并 fsync
		//    这样进程崩了下次也认得这个文件
		if err := p.Flush(); err != nil {
			p.Close()
			return nil, err
		}
	} else {
		// === 已存在数据库 ===
		// 1. 先 mmap 至少一个 chunk，才能读 page 0
		if err := p.growMmap(ChunkSize); err != nil {
			fp.Close()
			return nil, err
		}
		// 2. 从 page 0 读 nPages
		meta := p.GetPage(MetaPage)
		p.nPages = binary.LittleEndian.Uint64(meta[offNPages:])
		// 3. 根据 nPages 确保 mmap 足够大
		needBytes := int64(p.nPages) * PageSize
		if needBytes > p.fileSize {
			if err := p.growMmap(needBytes); err != nil {
				p.Close()
				return nil, err
			}
		}
	}

	return p, nil
}

func (p *Pager) Close() error {
	for _, ck := range p.chunks {
		_ = syscall.Munmap(ck)
	}
	p.chunks = nil
	return p.fp.Close()
}

// ─── GetPage / NewPage ─────────────────────────────────────────

// GetPage 返回第 id 页对应的 4096 字节 slice。
// 不做任何"合法性"检查——上层负责。
func (p *Pager) GetPage(id uint64) []byte {
	byteOff := int64(id) * PageSize
	chunkIdx := byteOff / ChunkSize
	inChunk := byteOff % ChunkSize
	return p.chunks[chunkIdx][inChunk : inChunk+PageSize]
}

// NewPage 分配一页新页，返回它的 id 和 slice。
// 内部：
//  1. id = 当前 nPages
//  2. nPages++
//  3. 如果文件 / mmap 不够大，growMmap
//
// TODO（你来写）：实现这 3 步
func (p *Pager) NewPage() (uint64, []byte, error) {
	// TODO 1: 取 id = p.nPages
	// TODO 2: p.nPages++
	// TODO 3: 计算新 nPages 需要多少字节 = p.nPages * PageSize
	//         如果超过 p.fileSize，调用 p.growMmap(...)
	// TODO 4: 返回 (id, p.GetPage(id), nil)
	id := p.nPages
	p.nPages++
	needBytes := int64(p.nPages * PageSize)
	if needBytes > p.fileSize {
		err := p.growMmap(needBytes)
		if err != nil {
			return 0, nil, err
		}
	}
	return id, p.GetPage(id), nil
}

// ─── Flush ─────────────────────────────────────────────────────

// Flush 持久化所有未落盘的修改。
// 流程：
//  1. 把当前 nPages 写回 page 0 的 meta 区
//  2. fsync 文件（确保所有 dirty page 落盘）
//
// 顺序很关键：必须先把 nPages 写进 page 0 的字节里，再 fsync 整个文件，
// 这样 fsync 才会把"更新后的 page 0"一起刷下去。
//
// TODO（你来写）
func (p *Pager) Flush() error {
	// TODO 1: meta := p.GetPage(MetaPage)
	// TODO 2: binary.LittleEndian.PutUint64(meta[offNPages:], p.nPages)
	// TODO 3: return p.fp.Sync()
	meta := p.GetPage(MetaPage)
	binary.LittleEndian.PutUint64(meta[offNPages:], p.nPages)
	return p.fp.Sync()
}

// ─── 内部：扩容 ─────────────────────────────────────────────────

// growMmap 把"文件大小 + 总映射大小"扩到至少 needBytes。
// 必要时 Truncate 文件、追加 chunk mmap。
func (p *Pager) growMmap(needBytes int64) error {
	// 向上对齐到 ChunkSize 的整数倍
	target := ((needBytes + ChunkSize - 1) / ChunkSize) * ChunkSize
	if target <= p.fileSize {
		return nil
	}
	if err := p.fp.Truncate(target); err != nil {
		return err
	}
	for int64(len(p.chunks))*ChunkSize < target {
		offset := int64(len(p.chunks)) * ChunkSize
		ck, err := syscall.Mmap(int(p.fp.Fd()), offset, ChunkSize,
			syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		p.chunks = append(p.chunks, ck)
	}
	p.fileSize = target
	return nil
}

// NPages 暴露给上层看（测试用）
func (p *Pager) NPages() uint64 { return p.nPages }
