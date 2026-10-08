package btree

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
)

var (
	// ErrTxReadOnly 表示在只读事务里写入或提交。
	ErrTxReadOnly = errors.New("btree: transaction is read-only")
	// ErrTxClosed 表示事务已经提交或回滚。
	ErrTxClosed = errors.New("btree: transaction is closed")
	// ErrTxFailed 表示事务中某次操作中途出错，事务内的树可能不一致，只能回滚。
	ErrTxFailed = errors.New("btree: transaction failed; roll back")
)

// tx 是一个事务：开始时复制 db 的 meta，事务内的树从这份 meta 的根出发。
//
// 写事务的新页先放在 dirty 里，页号从事务内的高水位分配；提交时统一写盘，
// 回滚时丢弃 dirty 与这份 meta 副本，db 的状态不变（做法同 bbolt 的 tx.init / tx.allocate）。
//
// 每张页在一个事务里只复制一次：已提交的页第一次修改时复制进 dirty，之后再修改就复用页号
// （见 txPageStore.replace）。由此带来三条约定：
//   - 事务内部不保留旧版本：同一事务里的旧根会被替换。已提交的版本与其他事务不受影响。
//   - put/delete 之后，本事务此前创建的游标失效，需要重新定位。
//   - 任何一次操作中途出错，事务只能回滚（failed）：可能叶子已替换而父页还没更新。
type tx struct {
	db       *db
	writable bool
	meta     meta              // 复制来的 meta；写事务在其上推进高水位
	dirty    map[uint64][]byte // 写事务新保存、尚未写盘的页：页号 → 整页
	tree     *tree             // 页存储是 txPageStore{本事务}
	closed   bool
	failed   error // 非 nil：某次操作中途出错，拒绝后续操作与提交

	committedHighWater uint64 // 事务开始时已提交的高水位：[firstDataPage, 它) 的页已在文件里

	holdsWriteLock bool // 持有 db.writeMu（经 begin 开始的写事务）
	holdsOpenLock  bool // 持有 db.openMu 的读锁（经 begin 开始的事务）

	// 页回收（写事务）。两者与 meta 副本一样：只在提交成功时发布到 db，回滚时直接丢弃。
	free  []uint64 // 可分配的空闲页号：开始时复制自 db.free，分配时取走，摘掉本事务新建的页时放回
	freed []uint64 // 本事务释放的已提交页：提交后进入 db.pending[txid]，等读者结束才能重用
}

// begin 开始一个事务。读事务不分配 dirty，save 会被拒绝。数据库已关闭时返回 ErrDBClosed。
//
// 读事务登记自己所读的版本号，结束时（rollback）注销：它用到的页在此期间不能被重用。
// 写事务开始时先把已经没有读者需要的待回收页放进空闲清单，再复制一份清单自用。
//
// 锁：写事务先取写者锁，一直持有到提交或回滚；所有事务持 openMu 的读锁直到结束，
// 让 close 等待。两者都在 release 中释放。
func (d *db) begin(writable bool) (*tx, error) {
	t := &tx{db: d, writable: writable}
	if writable {
		d.writeMu.Lock()
		t.holdsWriteLock = true
	}
	d.openMu.RLock()
	t.holdsOpenLock = true
	if d.closed {
		t.release()
		return nil, ErrDBClosed
	}

	// 下面这一段读写 d.meta 与 d.readers，要放在 d.stateMu 的保护下。
	// "复制 meta"与"登记版本号"必须在同一次持锁内完成，写事务的 releasePending 也在这把锁下，
	// 否则写事务可能在两步之间看到"没有读者"，过早重用这个读事务要读的页。
	t.db.stateMu.Lock()
	t.meta = d.meta
	if !writable && d.hookAfterMetaCopy != nil {
		d.hookAfterMetaCopy() // 测试钩子：这一行要紧跟在复制 meta 之后，不要移动
	}
	if writable {
		d.releasePending() // 读 d.readers，改 d.pending 与 d.free
	} else {
		d.readers[t.meta.txid]++
	}
	t.db.stateMu.Unlock()

	t.committedHighWater = t.meta.highWater
	if writable {
		t.dirty = make(map[uint64][]byte)
		t.meta.txid++ // 写事务的 txid = 已提交 txid + 1，写入另一个槽位（见 metaSlotFor）
		t.free = slices.Clone(d.free)
	}
	t.tree = &tree{store: txPageStore{t}, root: t.meta.root}
	return t, nil
}

// release 释放事务持有的锁。每个事务结束时恰好调用一次（commit 或 rollback）。
func (t *tx) release() {
	if t.holdsOpenLock {
		t.holdsOpenLock = false
		t.db.openMu.RUnlock()
	}
	if t.holdsWriteLock {
		t.holdsWriteLock = false
		t.db.writeMu.Unlock()
	}
}

// ─── 用户操作 ──────────────────────────────────────────────────

// get 查找 key。返回的 value 借用页内字节（dirty 页或只读映射），有效期到事务结束。
func (t *tx) get(key []byte) ([]byte, bool, error) {
	if t.closed {
		return nil, false, ErrTxClosed
	}
	if t.failed != nil {
		return nil, false, t.failed
	}
	return t.tree.get(key)
}

// put 插入或覆盖，只修改事务内的树；提交前对其他事务不可见，也不写盘。
func (t *tx) put(key, val []byte) error {
	if err := t.checkWritable(); err != nil {
		return err
	}
	err := t.tree.put(key, val)
	t.noteError(err)
	return err
}

// delete 删除 key，返回 key 是否存在。
func (t *tx) delete(key []byte) (bool, error) {
	if err := t.checkWritable(); err != nil {
		return false, err
	}
	found, err := t.tree.delete(key)
	t.noteError(err)
	return found, err
}

// noteError 在操作出错时把事务标记为失败。
// 输入被拒绝的两种错误除外：key 过长在动手之前就检查；记录过大在叶子层发现，
// 而页是自底向上替换的，此时还没有替换任何页，事务内的树完好，可以继续使用。
func (t *tx) noteError(err error) {
	if err == nil || errors.Is(err, ErrKeyTooLarge) || errors.Is(err, ErrRecordTooLarge) {
		return
	}
	t.failed = fmt.Errorf("%w: %v", ErrTxFailed, err)
}

func (t *tx) checkWritable() error {
	if t.closed {
		return ErrTxClosed
	}
	if !t.writable {
		return ErrTxReadOnly
	}
	return t.failed
}

// ─── 提交与回滚 ────────────────────────────────────────────────

// commit 把事务写到磁盘并发布。顺序（见 notes/05-storage-recovery/04）：
//
//	写全部 dirty 页 → sync（屏障）→ 写 meta → sync → 更新内存中的 db.meta
//
// 失败语义：
//   - 开始写 meta 之前失败：直接返回错误。db.meta 未变，相当于回滚；
//     已写的页在已提交的高水位之上，视为不存在，下一个事务会重新分配并覆盖它们。
//   - 开始写 meta 之后失败：磁盘上的 meta 可能已是新的，也可能仍是旧的，内存无法判断。
//     调用 db.fail 记录，之后拒绝写事务，只能关闭重开。
//
// 无论成功与否，事务都结束。
func (t *tx) commit() error {
	if err := t.checkWritable(); err != nil {
		if t.failed != nil {
			t.rollback() // 失败的事务不能提交，到此结束
		}
		return err
	}
	t.closed = true
	defer t.release() // 无论成功与否，事务到此结束
	if len(t.dirty) == 0 && t.tree.root == t.meta.root {
		return nil // 没有任何修改（例如只删了不存在的 key）：不写盘，不产生新版本
	}

	// 文件要覆盖到新的高水位：分配后又被摘掉的页不会写入，若它们恰在末尾，文件会比高水位短。
	if err := t.db.pager.Grow(int64(t.meta.highWater) * PageSize); err != nil {
		return err
	}
	// 页写入与同步经 t.db.writePage / t.db.sync，测试替换它们来注入故障。
	for _, id := range t.dirtyIDs() {
		err := t.db.writePage(id, t.dirty[id])
		if err != nil {
			return err
		}
	}
	err := t.db.sync()
	if err != nil {
		return err
	}
	t.meta.root = t.tree.root
	page := encodeMeta(t.meta)
	err = t.db.writePage(metaSlotFor(t.meta.txid), page)
	if err != nil {
		t.db.fail(err)
		return err
	}
	err = t.db.sync()
	if err != nil {
		t.db.fail(err)
		return err
	}
	// 发布 meta 要在 d.stateMu 的保护下——读事务开始时正在复制它。
	t.db.stateMu.Lock()
	t.db.meta = t.meta
	t.db.stateMu.Unlock()
	// 与 meta 一起发布页回收的结果：空闲清单换成本事务用过的那份；
	// 本事务释放的已提交页挂到自己的事务号下，等没有更早的读者时才能重用。
	t.db.free = t.free
	if len(t.freed) > 0 {
		t.db.pending[t.meta.txid] = t.freed
	}
	return nil
}

// rollback 丢弃事务：dirty 页、meta 副本、空闲清单副本与释放列表一起作废，db 不变。
// 读事务在这里注销登记。对已结束的事务无效果。
func (t *tx) rollback() {
	if t.closed {
		return
	}
	t.closed = true
	t.dirty = nil
	if !t.writable {
		// 注销登记要在 d.stateMu 的保护下——写事务开始时正在读 d.readers。
		t.db.stateMu.Lock()
		t.db.readers[t.meta.txid]--
		if t.db.readers[t.meta.txid] == 0 {
			delete(t.db.readers, t.meta.txid)
		}
		t.db.stateMu.Unlock()
	}
	t.release()
}

// dirtyIDs 返回按页号升序的 dirty 页号。升序写出接近顺序写；正确性不依赖这个顺序。
func (t *tx) dirtyIDs() []uint64 {
	ids := make([]uint64, 0, len(t.dirty))
	for id := range t.dirty {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ─── 事务页存储 ────────────────────────────────────────────────

// txPageStore 让树经事务读写页，实现 pageStore。单独成类型，是因为 tx 自己的 get 是按 key 查找。
type txPageStore struct {
	t *tx
}

// replace 保存 oldID 这一页的新版本，返回它所在的页号。
//
//	oldID 是本事务新建的页（在 dirty 里）→ 用新内容替换 dirty[oldID]，页号不变
//	否则（已提交的页）                   → 同 save：分配新页号，旧页不动；
//	                                      旧页记入释放列表，提交后等读者结束才能重用
//
// 本事务新建的页还没写进文件，没有 meta 指向它，其他事务看不到，所以可以复用页号；
// 已提交的页可能有读事务在看，崩溃后也可能要退回到它，必须复制。
// 复用的是页号，不是字节：dirty 里换进去的是新切片，之前借出的旧切片内容不变。
func (s txPageStore) replace(oldID uint64, page []byte) (uint64, error) {
	if err := s.t.checkWritable(); err != nil {
		return 0, err
	}
	if len(page) != PageSize {
		return 0, fmt.Errorf("%w: got %d bytes", ErrBadPageSize, len(page))
	}
	if _, owned := s.t.dirty[oldID]; !owned {
		newID, err := s.save(page)
		if err != nil {
			return 0, err
		}
		s.t.freed = append(s.t.freed, oldID) // 旧版本的页：新版本不再引用它
		return newID, nil
	}
	s.t.dirty[oldID] = bytes.Clone(page)
	return oldID, nil
}

// free 处理从树上摘掉的页：
//
//	本事务新建的页（在 dirty 里）→ 从 dirty 删除，页号放回空闲清单副本，本事务内立即可重用；
//	                               它从未被任何人看到，不会写盘
//	已提交的页                   → 记入释放列表，提交后等读者结束才能重用
func (s txPageStore) free(id uint64) error {
	if err := s.t.checkWritable(); err != nil {
		return err
	}
	if _, owned := s.t.dirty[id]; owned {
		delete(s.t.dirty, id)
		s.t.free = append(s.t.free, id)
		return nil
	}
	s.t.freed = append(s.t.freed, id)
	return nil
}

// save 复制 page，分配页号，放进 dirty，不写盘。
// 页号优先取空闲清单副本中的页（重用），没有空闲页才推进事务内的高水位。
func (s txPageStore) save(page []byte) (uint64, error) {
	if err := s.t.checkWritable(); err != nil {
		return 0, err
	}
	if len(page) != PageSize {
		return 0, fmt.Errorf("%w: got %d bytes", ErrBadPageSize, len(page))
	}
	var id uint64
	if last := len(s.t.free) - 1; last >= 0 {
		id = s.t.free[last]
		s.t.free = s.t.free[:last]
	} else {
		id = s.t.meta.highWater
		s.t.meta.highWater++
	}
	s.t.dirty[id] = bytes.Clone(page)
	return id, nil
}

// get 先查本事务的 dirty 页，再从只读映射读已提交的页。
// 已提交范围是 [firstDataPage, committedHighWater)；范围外的页号不去碰映射，直接报缺页。
func (s txPageStore) get(id uint64) ([]byte, error) {
	if s.t.closed {
		return nil, ErrTxClosed
	}
	if page, ok := s.t.dirty[id]; ok {
		return page, nil
	}
	if id < firstDataPage || id >= s.t.committedHighWater {
		return nil, fmt.Errorf("%w: page %d", ErrPageNotFound, id)
	}
	return s.t.db.pager.ReadPage(id)
}
