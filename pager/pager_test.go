package pager

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"testing"
)

// filledPage 返回每个字节都是 b 的整页，便于检查整页内容。
func filledPage(b byte) []byte {
	return bytes.Repeat([]byte{b}, PageSize)
}

// 写入某页 → 同一 Pager 经映射读回 → 关闭重开后仍能读回。
func TestWriteReadAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pager.db")
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.FileSize() != 0 {
		t.Fatalf("新文件长度 = %d，期望 0", p.FileSize())
	}
	if err := p.WritePage(1, filledPage(7)); err != nil {
		t.Fatal(err)
	}
	got, err := p.ReadPage(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, filledPage(7)) {
		t.Fatal("WriteAt 后经映射读到的内容不对")
	}
	if err := p.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.ReadPage(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, filledPage(7)) {
		t.Fatal("重开后内容不对")
	}
}

// 覆盖写同一页：之前借出的切片指向同一段映射，能看到新内容。
// 这说明借用切片不是快照；CoW 的上层保证活页不被覆盖写。
func TestOverwriteVisibleThroughMapping(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "pager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.WritePage(0, filledPage(1)); err != nil {
		t.Fatal(err)
	}
	view, err := p.ReadPage(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WritePage(0, filledPage(2)); err != nil {
		t.Fatal(err)
	}
	if view[0] != 2 {
		t.Fatalf("旧视图首字节 = %d，期望看到新写入的 2", view[0])
	}
}

// 跨多个 chunk 写入：chunk = 4 页，写到第 10 页需要 3 个 chunk；旧 chunk 的视图仍有效。
func TestGrowAcrossChunks(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "pager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.WritePage(0, filledPage(100)); err != nil {
		t.Fatal(err)
	}
	firstView, err := p.ReadPage(0)
	if err != nil {
		t.Fatal(err)
	}
	for id := uint64(1); id <= 10; id++ {
		if err := p.WritePage(id, filledPage(byte(id))); err != nil {
			t.Fatal(err)
		}
	}
	wantSize := int64(3 * ChunkSize)
	if p.FileSize() != wantSize {
		t.Fatalf("文件长度 = %d，期望 %d（chunk 对齐）", p.FileSize(), wantSize)
	}
	for id := uint64(1); id <= 10; id++ {
		got, err := p.ReadPage(id)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, filledPage(byte(id))) {
			t.Fatalf("page %d 内容不对", id)
		}
	}
	if firstView[0] != 100 {
		t.Fatal("扩容后 page 0 的旧视图失效")
	}
}

// 扩容和重开组合：每页完整内容跨越 Close/Open 后保持，重开后继续写、再次重开也能读回。
func TestReopenAcrossChunksPreservesPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.db")
	nextID := uint64(0)
	for _, count := range []int{10, 4} {
		p, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			if err := p.WritePage(nextID, filledPage(byte(nextID))); err != nil {
				t.Fatal(err)
			}
			nextID++
		}
		if err := p.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}

		reopened, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for id := uint64(0); id < nextID; id++ {
			got, err := reopened.ReadPage(id)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, filledPage(byte(id))) {
				t.Fatalf("重开后 page %d 内容改变", id)
			}
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Open 不改变文件长度：既不截掉尾部，也不为对齐而扩长（包括不是 chunk 倍数的文件）。
// 之后写入尚未映射的 chunk 时，文件已够长也要补建映射。
func TestOpenPreservesFileLength(t *testing.T) {
	for _, size := range []int{4 * ChunkSize, ChunkSize + PageSize} {
		path := filepath.Join(t.TempDir(), "capacity.db")
		data := make([]byte, size)
		marker := []byte("keep file tail")
		copy(data[len(data)-len(marker):], marker)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		p, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != int64(size) {
			t.Fatalf("Open 改变文件长度：%d → %d", size, info.Size())
		}
		lastPage := uint64(size/PageSize - 1)
		got, err := p.ReadPage(lastPage)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[PageSize-len(marker):], marker) {
			t.Fatal("读不到文件尾部内容")
		}
		if err := p.WritePage(1, filledPage(9)); err != nil {
			t.Fatal(err)
		}
		p.Close()
		got2, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(got2) != size || !bytes.Equal(got2[len(got2)-len(marker):], marker) {
			t.Fatal("写入页破坏了文件尾部或改变了长度")
		}
	}
}

// 读取越过文件末尾的页返回错误，而不是访问映射触发 SIGBUS。
func TestReadPageOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.db")
	if err := os.WriteFile(path, make([]byte, PageSize+100), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.ReadPage(0); err != nil {
		t.Fatalf("page 0 完整在文件内，应可读：%v", err)
	}
	// page 1 只有 100 字节在文件内，映射到了但后半越过文件末尾。
	for _, id := range []uint64{1, 2, 1000} {
		if _, err := p.ReadPage(id); !errors.Is(err, ErrPageOutOfRange) {
			t.Fatalf("page %d: err = %v，期望 ErrPageOutOfRange", id, err)
		}
	}
}

func TestWritePageRejectsBadSize(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "pager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, size := range []int{0, PageSize - 1, PageSize + 1} {
		if err := p.WritePage(0, make([]byte, size)); !errors.Is(err, ErrBadPageSize) {
			t.Fatalf("size %d: err = %v", size, err)
		}
	}
	if p.FileSize() != 0 {
		t.Fatal("拒绝写入时不应扩文件")
	}
}

// 映射是只读的：写 ReadPage 借出的切片触发内存保护错误，而不是悄悄改掉文件。
// SetPanicOnFault 把这次访问错误转成可 recover 的 panic，测试才能继续运行。
func TestReadPageViewIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pager.db")
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.WritePage(0, filledPage(5)); err != nil {
		t.Fatal(err)
	}
	view, err := p.ReadPage(0)
	if err != nil {
		t.Fatal(err)
	}

	faulted := func() (faulted bool) {
		old := debug.SetPanicOnFault(true)
		defer debug.SetPanicOnFault(old)
		defer func() {
			if recover() != nil {
				faulted = true
			}
		}()
		view[0] = 6
		return false
	}()
	if !faulted {
		t.Fatal("写只读映射没有触发错误")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 5 {
		t.Fatalf("文件首字节 = %d，期望仍为 5", got[0])
	}
}

// 一个写者不断扩容（追加 chunk）的同时，多个读者不加锁地读已有的页：
// 读者看到的映射列表始终完整，旧 chunk 的内容不受扩容影响。用 go test -race 运行才有意义。
func TestConcurrentReadWhileGrowing(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "pager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	const stablePages = 8 // 先写好两个 chunk，读者只读这一部分
	for id := uint64(0); id < stablePages; id++ {
		if err := p.WritePage(id, filledPage(byte(id+1))); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	var readers sync.WaitGroup
	errs := make(chan error, 4)
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				for id := uint64(0); id < stablePages; id++ {
					page, err := p.ReadPage(id)
					if err != nil || page[0] != byte(id+1) || page[PageSize-1] != byte(id+1) {
						errs <- errors.New("读者读到错误的页")
						return
					}
				}
			}
		}()
	}
	for id := uint64(stablePages); id < 400; id++ { // 100 个 chunk
		if err := p.WritePage(id, filledPage(0xee)); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	readers.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}
