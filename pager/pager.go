// Package pager 提供"按页读写文件"的能力：读经只读 mmap，写经 WriteAt。
//
// 设计（见 README.md 与 notes/02-cache-pager、notes/05-storage-recovery/04）：
//   - 文件按 4KB 分页，页号 id 对应文件偏移 id*PageSize
//   - 用分段 mmap（chunk）避免扩容时旧映射失效；映射只读（PROT_READ）
//   - 写入只走 WriteAt，何时写、何时 Sync 由上层决定，Pager 不自行刷盘
//
// Pager 不理解页内容，也不管理 meta、页号分配或提交顺序；这些属于上层（btree 的 db/tx）。
//
// 对外 API：
//
//	Open(path)            → *Pager
//	ReadPage(id)          → 借用的只读 []byte（4096 字节）
//	WritePage(id, page)   → WriteAt，必要时先扩文件与映射
//	Sync()                → fsync 文件
//	Grow(needBytes)       → 扩文件并补映射
//	FileSize() / Close()
package pager

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
)

const (
	PageSize  = 4096
	ChunkSize = 16 * 1024 // 演示值；生产代码用更大的段
)

// ErrPageOutOfRange 表示页号超出文件范围：读取越过文件末尾的映射会触发 SIGBUS，因此先检查。
var ErrPageOutOfRange = errors.New("pager: page out of file range")

// ErrBadPageSize 表示写入的页不是 PageSize 字节。
var ErrBadPageSize = errors.New("pager: page size is not PageSize")

// Pager 的并发约定：ReadPage 可以被多个 goroutine 同时调用，并且可以与一个写者的
// WritePage、Grow、Sync 并发；写者同一时刻只能有一个（由上层的写者锁保证）；
// Close 必须在所有读写都结束之后调用（由上层保证）。
//
// 读者不加锁：映射列表是不可变的，扩容时复制一份、在末尾追加新 chunk，再原子地换上去
// （写时复制）。旧列表中的 chunk 从不解除映射，读者手里的旧列表与旧切片一直有效。
type Pager struct {
	fp       *os.File
	fileSize atomic.Int64             // 文件实际长度；已映射字节数独立由 len(chunks)*ChunkSize 表示
	chunks   atomic.Pointer[[][]byte] // 当前的映射列表：每个 chunk 一次独立的只读 mmap；列表本身不可变
}

// ─── Open / Close ──────────────────────────────────────────────

// Open 打开或创建文件，映射覆盖当前文件长度的 chunk。
//
// Open 不改变文件长度，也不解释文件内容：空文件、他人的文件都原样打开，
// 格式校验由上层读 meta 完成。最后一个 chunk 可能越过文件末尾，
// 越界部分不可访问，ReadPage 按文件长度检查页号。
func Open(path string) (*Pager, error) {
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	info, err := fp.Stat()
	if err != nil {
		fp.Close()
		return nil, err
	}
	p := &Pager{fp: fp}
	if err := p.mapUpTo(info.Size()); err != nil {
		p.Close()
		return nil, err
	}
	p.fileSize.Store(info.Size())
	return p, nil
}

// Close 解除全部映射并关闭文件。之后 ReadPage 返回过的切片都不可再用。
// 不隐式 Sync：是否持久由上层在提交时决定。
func (p *Pager) Close() error {
	p.fileSize.Store(0) // 之后的 ReadPage 一律报越界，不会去碰已解除的映射
	for _, ck := range p.currentChunks() {
		_ = syscall.Munmap(ck)
	}
	p.chunks.Store(nil)
	return p.fp.Close()
}

// currentChunks 返回当前的映射列表。返回的切片不可修改。
func (p *Pager) currentChunks() [][]byte {
	if list := p.chunks.Load(); list != nil {
		return *list
	}
	return nil
}

// ─── 读写 ──────────────────────────────────────────────────────

// ReadPage 返回第 id 页的只读视图，借用映射内存，不复制。
//
// 有效期到 Close 为止：chunk 一旦映射就不移动，扩容只追加新 chunk。
// 调用者不能写这个切片：映射是只读的，写入会触发段错误，而不是悄悄改掉数据库文件。
// WritePage 写同一页后，经 MAP_SHARED 映射能看到新内容（统一页缓存）。
func (p *Pager) ReadPage(id uint64) ([]byte, error) {
	// 先读长度、后取列表：扩容时先换列表、后更新长度，所以读到新长度时，
	// 取到的列表一定已经包含对应的 chunk。
	fileSize := p.fileSize.Load()
	end := (int64(id) + 1) * PageSize
	if end > fileSize {
		return nil, fmt.Errorf("%w: page %d, file %d bytes", ErrPageOutOfRange, id, fileSize)
	}
	chunks := p.currentChunks()
	byteOff := int64(id) * PageSize
	chunkIdx := byteOff / ChunkSize
	inChunk := byteOff % ChunkSize
	return chunks[chunkIdx][inChunk : inChunk+PageSize], nil
}

// WritePage 用 WriteAt 把 page 写到第 id 页，只进入页缓存，不代表已落盘。
// 页超出文件范围时，先把文件扩到 chunk 对齐的长度并补齐映射，再写入。
func (p *Pager) WritePage(id uint64, page []byte) error {
	if len(page) != PageSize {
		return fmt.Errorf("%w: got %d bytes", ErrBadPageSize, len(page))
	}
	end := (int64(id) + 1) * PageSize
	if err := p.Grow(end); err != nil {
		return err
	}
	_, err := p.fp.WriteAt(page, int64(id)*PageSize)
	return err
}

// Sync 把文件已写入页缓存的内容（含文件长度）刷到磁盘，返回后才算持久。
// 不保证内部刷写顺序；需要顺序时由上层分两次 Sync。
func (p *Pager) Sync() error {
	return p.fp.Sync()
}

// FileSize 返回文件实际长度。它不是数据库边界：哪些页有效由上层的 meta 决定。
func (p *Pager) FileSize() int64 { return p.fileSize.Load() }

// ─── 内部：扩容与映射 ───────────────────────────────────────────

// Grow 确保文件长度至少为 needBytes（向上对齐到 chunk），并建立对应映射。文件只扩不缩。
// WritePage 会自动调用；上层也可以直接调用，让文件覆盖尚未写入的页（它们读出来是全零）。
// 扩长只进入页缓存，随下一次 Sync 持久。
func (p *Pager) Grow(needBytes int64) error {
	fileSize := p.fileSize.Load()
	if needBytes > fileSize {
		target := ((needBytes + ChunkSize - 1) / ChunkSize) * ChunkSize
		if err := p.fp.Truncate(target); err != nil {
			return err
		}
		fileSize = target
	}
	// 先让映射列表覆盖新长度，再公布新长度（见 ReadPage）。
	if err := p.mapUpTo(fileSize); err != nil {
		return err
	}
	p.fileSize.Store(fileSize)
	return nil
}

// mapUpTo 追加 chunk 映射，直到映射范围覆盖 size 字节。
// 文件长度够大，不代表当前进程已经建立了对应映射，二者分别管理。
//
// 不修改已公布的列表：在副本上追加，全部映射成功后原子地换上新列表。
// 只由唯一的写者（或 Open）调用。
func (p *Pager) mapUpTo(size int64) error {
	current := p.currentChunks()
	if int64(len(current))*ChunkSize >= size {
		return nil
	}
	grown := make([][]byte, len(current), len(current)+1)
	copy(grown, current)
	for int64(len(grown))*ChunkSize < size {
		offset := int64(len(grown)) * ChunkSize
		ck, err := syscall.Mmap(int(p.fp.Fd()), offset, ChunkSize,
			syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			p.chunks.Store(&grown) // 已建立的映射仍要记下，Close 时才能解除
			return err
		}
		grown = append(grown, ck)
	}
	p.chunks.Store(&grown)
	return nil
}
