package btree

import (
	"bytes"
	"errors"
	"fmt"
)

// ErrBadPageSize 表示保存的页不是完整的 PageSize 字节；存储保持不变。
var ErrBadPageSize = errors.New("btree: page size is not PageSize")

// ErrPageNotFound 表示按页号找不到页。这是页引用或存取错误，不表示用户 key 不存在。
var ErrPageNotFound = errors.New("btree: page not found")

// pageStore 是树对页存储的全部要求：保存新页得到页号，按页号借用读取。
//
// 约定（所有实现都遵守）：
//   - save 复制输入并分配一个从未用过的新页号；页号 0 永远不会分配，表示"没有页"。
//   - replace 保存 oldID 这一页的新版本，返回新版本所在的页号。实现可以分配新页号
//     （同 save），也可以在确认 oldID 没有别人能看到时复用 oldID。无论哪种，
//     replace 之前经 get 借出的旧版本切片内容不变。
//   - free 告知 id 这一页已从树上摘掉、不再被引用。实现可以回收页号，也可以什么都不做。
//     调用后树不再访问这个页号；被 replace 替换下来的旧页由实现自己处理，不需要再 free。
//   - get 返回借用的只读视图；页号不存在返回 ErrPageNotFound。
//   - 别人可能看到的页不会被覆盖：已提交的页、内存页存储中的所有页都如此。
//
// 实现：memPageStore（纯内存，单元测试用）；txPageStore（事务页存储，接到磁盘文件，见 tx.go）。
type pageStore interface {
	save(page []byte) (uint64, error)
	replace(oldID uint64, page []byte) (uint64, error)
	free(id uint64) error
	get(id uint64) ([]byte, error)
}

// memPageStore 是内存页存储：页号 → 整页字节。
//
// 约定：
//   - 页号由 save 分配，从 1 开始递增；0 表示"没有页"，永远不会分配。
//   - save 复制输入，之后调用者修改输入不影响已保存的页。
//   - get 返回存储内部字节的借用视图，调用者只能读不能改。
//   - 已保存的页不能被覆盖，也暂不回收；CoW 更新总是 save 出新页号。
//
// 不是并发安全的；暂不提供删除、回收和落盘，这些在后续知识点中处理。
type memPageStore struct {
	pages  map[uint64][]byte
	nextID uint64 // 下一次 save 分配的页号
}

func newMemPageStore() *memPageStore {
	return &memPageStore{
		pages:  make(map[uint64][]byte),
		nextID: 1, // 0 留作"没有页"
	}
}

// save 复制 page 并分配一个新页号。page 长度必须为 PageSize，否则返回 0, ErrBadPageSize。
// 页号用 uint64 递增，每页 4 KiB 时实际不可能耗尽，因此不检查回绕。
func (s *memPageStore) save(page []byte) (uint64, error) {
	if len(page) != PageSize {
		return 0, fmt.Errorf("%w: got %d bytes", ErrBadPageSize, len(page))
	}
	id := s.nextID
	ownCopy := bytes.Clone(page) // 存储拥有自己的字节，不与调用者共享底层数组
	s.pages[id] = ownCopy
	s.nextID++
	return id, nil
}

// replace 总是分配新页号：内存页存储里每个旧根都是完整的旧版本（快照），任何页都可能有人在看。
func (s *memPageStore) replace(oldID uint64, page []byte) (uint64, error) {
	return s.save(page)
}

// free 什么都不做：内存页存储不回收，摘掉的页仍属于某个旧版本，可以继续读。
func (s *memPageStore) free(id uint64) error {
	return nil
}

// get 返回页号 id 对应页的借用只读视图。页号不存在（包括 0）返回 nil, ErrPageNotFound。
func (s *memPageStore) get(id uint64) ([]byte, error) {
	page, ok := s.pages[id]
	if !ok {
		return nil, fmt.Errorf("%w: page %d", ErrPageNotFound, id)
	}
	return page, nil
}
