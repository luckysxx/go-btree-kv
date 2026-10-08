package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"

	"dbstudy/pager"
)

// 文件布局：页 0、页 1 是两个 meta 槽位，轮流写；树的页从 2 开始。
const (
	metaSlots     = 2
	firstDataPage = 2
)

// meta 页内字段（小端）。magic、version、pageSize 是格式常量，只在编码/校验时出现。
const (
	metaMagic   uint32 = 0x4b564442 // "BDVK"：确认文件是本数据库
	metaVersion uint32 = 2          // 文件格式版本；格式改变时递增（2：加入 txid 与校验和）

	offMetaMagic     = 0  // uint32
	offMetaVersion   = 4  // uint32
	offMetaPageSize  = 8  // uint32
	offMetaRoot      = 16 // uint64；12～15 保留
	offMetaHighWater = 24 // uint64
	offMetaTxid      = 32 // uint64
	offMetaChecksum  = 40 // uint64：FNV-64a，覆盖 [0, offMetaChecksum)
)

var (
	// ErrInvalidDB 表示文件不是可用的数据库：magic 不符、页大小不符、meta 字段越界或文件比高水位短。
	ErrInvalidDB = errors.New("btree: invalid database file")
	// ErrVersionMismatch 表示文件格式版本与本程序不同。
	ErrVersionMismatch = errors.New("btree: database version mismatch")
	// ErrMetaChecksum 表示 meta 的校验和不符：撕裂写、位翻转或其他损坏。
	ErrMetaChecksum = errors.New("btree: meta checksum mismatch")
	// ErrDBClosed 表示数据库已经关闭，不能再开始事务。
	ErrDBClosed = errors.New("btree: database is closed")
	// ErrDBFailed 表示某次提交在开始写 meta 之后失败，磁盘上的 meta 可能是旧的也可能是新的。
	// 内存状态无法判断磁盘上是哪一个，此后拒绝写事务，只能关闭后重开，由 Open 按磁盘内容决定。
	ErrDBFailed = errors.New("btree: database failed during commit; close and reopen")
)

// meta 是一次提交发布的全部内容。根页号与高水位必须由同一次 meta 写入发布：
// 重启只相信 meta，高水位以上的页一律视为不存在（见 notes/05-storage-recovery/02）。
type meta struct {
	root      uint64 // 根页号
	highWater uint64 // 高水位：下一个可分配的页号，[firstDataPage, highWater) 是已分配的页
	txid      uint64 // 事务号：每次写事务 +1；写入槽位 metaSlotFor(txid)
}

// metaSlotFor 返回事务号 txid 的 meta 应写入的页号（0 或 1）。
//
// 写事务的 txid = 已提交 txid + 1，所以它总落在已提交那份之外的槽位：
// 永远不覆盖最新的有效 meta。前提是内存中的已提交 meta 等于磁盘上最新的有效 meta，
// 写 meta 之后的失败会打破这一点，因此进入 ErrDBFailed（见 notes/05-storage-recovery/03）。
func metaSlotFor(txid uint64) uint64 {
	return txid % metaSlots
}

// metaChecksum 计算 meta 页 [0, offMetaChecksum) 的 FNV-64a（同 bbolt）。
func metaChecksum(page []byte) uint64 {
	h := fnv.New64a()
	h.Write(page[:offMetaChecksum])
	return h.Sum64()
}

// encodeMeta 把 m 编码成一整页，末尾写入校验和，未使用的字节为 0。
func encodeMeta(m meta) []byte {
	page := make([]byte, PageSize)
	binary.LittleEndian.PutUint32(page[offMetaMagic:], metaMagic)
	binary.LittleEndian.PutUint32(page[offMetaVersion:], metaVersion)
	binary.LittleEndian.PutUint32(page[offMetaPageSize:], PageSize)
	binary.LittleEndian.PutUint64(page[offMetaRoot:], m.root)
	binary.LittleEndian.PutUint64(page[offMetaHighWater:], m.highWater)
	binary.LittleEndian.PutUint64(page[offMetaTxid:], m.txid)
	binary.LittleEndian.PutUint64(page[offMetaChecksum:], metaChecksum(page))
	return page
}

// decodeMeta 解码并校验槽位 slot 上的 meta 页，顺序：
//
//	magic（是不是本库文件）→ version（布局不同则无法算校验和）→ 校验和（是否完整）
//	→ 页大小、槽位与 txid 相符、根与高水位的范围
//
// 槽位检查（txid % 2 == slot）能发现写到错误位置的 meta。
func decodeMeta(page []byte, slot uint64) (meta, error) {
	magic := binary.LittleEndian.Uint32(page[offMetaMagic:])
	if magic != metaMagic {
		return meta{}, fmt.Errorf("%w: magic %#x", ErrInvalidDB, magic)
	}
	version := binary.LittleEndian.Uint32(page[offMetaVersion:])
	if version != metaVersion {
		return meta{}, fmt.Errorf("%w: file version %d, program version %d", ErrVersionMismatch, version, metaVersion)
	}
	stored := binary.LittleEndian.Uint64(page[offMetaChecksum:])
	if computed := metaChecksum(page); stored != computed {
		return meta{}, fmt.Errorf("%w: stored %#x, computed %#x", ErrMetaChecksum, stored, computed)
	}
	pageSize := binary.LittleEndian.Uint32(page[offMetaPageSize:])
	if pageSize != PageSize {
		return meta{}, fmt.Errorf("%w: page size %d", ErrInvalidDB, pageSize)
	}
	m := meta{
		root:      binary.LittleEndian.Uint64(page[offMetaRoot:]),
		highWater: binary.LittleEndian.Uint64(page[offMetaHighWater:]),
		txid:      binary.LittleEndian.Uint64(page[offMetaTxid:]),
	}
	if metaSlotFor(m.txid) != slot {
		return meta{}, fmt.Errorf("%w: txid %d in slot %d", ErrInvalidDB, m.txid, slot)
	}
	if m.root < firstDataPage || m.root >= m.highWater {
		return meta{}, fmt.Errorf("%w: root %d outside [%d, %d)", ErrInvalidDB, m.root, firstDataPage, m.highWater)
	}
	return m, nil
}

// readMetaSlot 读出并校验槽位 slot。文件太短、读不到该页也算这个槽位无效。
func readMetaSlot(p *pager.Pager, slot uint64) (meta, error) {
	page, err := p.ReadPage(slot)
	if err != nil {
		return meta{}, fmt.Errorf("%w: %v", ErrInvalidDB, err)
	}
	return decodeMeta(page, slot)
}

// pickMeta 从两个槽位的解码结果中选出打开时使用的 meta：
//
//	两份都有效     → txid 较大者
//	只有一份有效   → 它
//	都无效         → 返回 bothMetasInvalid(err0, err1)
//
// 只信有效的那份是安全的：无效的只可能是正在写、未确认的那次提交。
func pickMeta(m0 meta, err0 error, m1 meta, err1 error) (meta, error) {
	valid0 := err0 == nil
	valid1 := err1 == nil
	switch {
	case valid0 && valid1:
		// 两个槽位的 txid 奇偶不同（decodeMeta 检查过槽位），不会相等。
		if m0.txid > m1.txid {
			return m0, nil
		}
		return m1, nil
	case valid0:
		return m0, nil
	case valid1:
		return m1, nil
	default:
		return meta{}, bothMetasInvalid(err0, err1)
	}
}

// bothMetasInvalid 组合两个槽位的错误。两份都是版本不符时报 ErrVersionMismatch
// （整个文件是另一版本的格式），否则报 ErrInvalidDB，并带上两份各自的原因。
func bothMetasInvalid(err0, err1 error) error {
	if errors.Is(err0, ErrVersionMismatch) && errors.Is(err1, ErrVersionMismatch) {
		return err0
	}
	return fmt.Errorf("%w: no valid meta (slot 0: %v; slot 1: %v)", ErrInvalidDB, err0, err1)
}

// db 是打开的数据库文件：Pager 加上最近一次提交成功的 meta。
//
// 并发：可以被多个 goroutine 同时使用。同一时刻只有一个写事务，读事务数量不限，
// 读者之间、读者与写者之间互不等待。一个事务只能由一个 goroutine 使用。
// 读事务借出的 value 指向只读映射，有效期到事务结束（见 tx.get）。
type db struct {
	pager  *pager.Pager
	meta   meta  // 最近一次提交成功的 meta；新事务从它复制。由 stateMu 保护
	failed error // 非 nil：提交在写 meta 之后失败，拒绝后续写事务。只有写事务访问

	// 锁（见 notes/05-storage-recovery/08）。加锁顺序：writeMu → openMu（读锁）→ stateMu。
	writeMu sync.Mutex   // 写者锁：同一时刻只有一个写事务，从事务开始持有到提交或回滚
	stateMu sync.Mutex   // 状态锁：保护 meta 与 readers，每次只持有几行代码的时间
	openMu  sync.RWMutex // 每个事务从开始到结束持读锁；close 取写锁，等所有事务结束
	closed  bool         // 已关闭，由 openMu 保护

	// 页回收（见 notes/05-storage-recovery/07）。三者只存在于内存，打开时由 rebuildFree 重算。
	free    []uint64            // 现在就可以重用的页号。只有写事务访问
	pending map[uint64][]uint64 // 事务号 → 该事务释放的已提交页；还可能有更早的读者在看。只有写事务访问
	readers map[uint64]int      // 读事务所读的版本号 → 打开的读事务个数。由 stateMu 保护

	// 页写入与同步都经过这两个函数，默认直接调用 Pager；测试替换它们来注入故障、记录顺序。
	writePage func(id uint64, page []byte) error
	sync      func() error

	// 测试钩子，平时为 nil：读事务开始时，在"复制 meta"之后、"登记版本号"之前调用。
	// 测试用它在这两步之间插入写事务，检验两步是否在同一次持锁内完成（见 begin）。
	hookAfterMetaCopy func()
}

// openDB 打开或创建数据库文件。
//
// 空文件：用一次普通提交写入空叶子根与第一份 meta，初始化走的也是 tx.commit 的顺序。
// 已有文件：只相信 meta。两个槽位分别校验，用 pickMeta 选出一份，再要求文件至少覆盖其高水位；
// 文件比高水位长（失败事务写过的页）是正常的，那些页视为不存在，之后被重新分配覆盖。
// 打开失败不修改文件。
func openDB(path string) (*db, error) {
	p, err := pager.Open(path)
	if err != nil {
		return nil, err
	}
	d := &db{
		pager:     p,
		writePage: p.WritePage,
		sync:      p.Sync,
		pending:   make(map[uint64][]uint64),
		readers:   make(map[uint64]int),
	}

	if p.FileSize() == 0 {
		if err := d.init(); err != nil {
			p.Close()
			return nil, err
		}
		return d, nil
	}

	m0, err0 := readMetaSlot(p, 0)
	m1, err1 := readMetaSlot(p, 1)
	m, err := pickMeta(m0, err0, m1, err1)
	if err != nil {
		p.Close()
		return nil, err
	}
	needBytes := int64(m.highWater) * PageSize
	if p.FileSize() < needBytes {
		p.Close()
		return nil, fmt.Errorf("%w: file %d bytes, high water needs %d", ErrInvalidDB, p.FileSize(), needBytes)
	}
	d.meta = m
	if err := d.rebuildFree(); err != nil {
		p.Close()
		return nil, err
	}
	return d, nil
}

// rebuildFree 在打开时重算空闲清单：[firstDataPage, 高水位) 中从根不可达的页都是空闲的。
// 此时没有任何读事务，这些页立即全部可用，pending 为空。
// 只需读内部页：叶子的页号写在父页里，叶子本身不用读。
//
// 只看被选中的这份 meta：另一个槽位中上一版用到的页也会被算作空闲，所以退路只保证一版。
func (d *db) rebuildFree() error {
	reachable := make(map[uint64]bool)
	if err := d.markReachable(d.meta.root, reachable); err != nil {
		return err
	}
	d.free = d.free[:0]
	for id := uint64(firstDataPage); id < d.meta.highWater; id++ {
		if !reachable[id] {
			d.free = append(d.free, id)
		}
	}
	return nil
}

// markReachable 把以 id 为根的子树中的全部页号记入 reachable。
// 页号越界或被引用两次（含环）说明树已损坏。
func (d *db) markReachable(id uint64, reachable map[uint64]bool) error {
	if id < firstDataPage || id >= d.meta.highWater {
		return fmt.Errorf("%w: page %d outside [%d, %d)", ErrCorruptTree, id, firstDataPage, d.meta.highWater)
	}
	if reachable[id] {
		return fmt.Errorf("%w: page %d referenced more than once", ErrCorruptTree, id)
	}
	reachable[id] = true
	page, err := d.pager.ReadPage(id)
	if err != nil {
		return err
	}
	if nodeType(page) != NodeInternal {
		return nil // 叶子（或未知类型，留给 check 报告）：没有孩子
	}
	for i := 0; i < int(nodeNKeys(page)); i++ {
		if err := d.markReachable(internalChildAt(page, i), reachable); err != nil {
			return err
		}
	}
	return nil
}

// minReaderTxid 返回当前打开的读事务中最小的版本号；没有读事务时 ok 为 false。
func (d *db) minReaderTxid() (txid uint64, ok bool) {
	for readerTxid := range d.readers {
		if !ok || readerTxid < txid {
			txid, ok = readerTxid, true
		}
	}
	return txid, ok
}

// releasePending 把已经没有读者需要的待回收页移入空闲清单，在写事务开始时调用。
//
// 规则：事务 T 释放的页属于版本 T-1 或更早，只有版本号小于 T 的读事务还可能在看。
//
//	没有读事务               → 全部放出
//	T <= 最小的读者版本号    → pending[T] 可以放出
//	T >  最小的读者版本号    → 留着，那个读者还可能在看
//
// 调用时，待回收表里的事务都已经提交成功（pending 只在提交成功后写入），
// 所以"崩溃后要退回的版本"不会引用这些页。
func (d *db) releasePending() {
	minReader, hasReader := d.minReaderTxid()
	for txid, pages := range d.pending {
		if txid > minReader && hasReader {
			continue
		}
		d.free = append(d.free, pages...)
		delete(d.pending, txid)
	}
}

// init 为新文件写入空树：一个写事务（txid 1，写入页 1），保存空叶子作根并提交。
// 页 0 保持全零（无效）；之后第一次提交（txid 2）写页 0，此时页 1 是退路。
// 中途崩溃会留下没有有效 meta 的文件，之后 Open 返回 ErrInvalidDB。
func (d *db) init() error {
	t := &tx{db: d, writable: true, meta: meta{highWater: firstDataPage, txid: 1}, dirty: make(map[uint64][]byte)}
	tr, err := newTree(txPageStore{t})
	if err != nil {
		return err
	}
	t.tree = tr
	return t.commit()
}

// close 等待所有事务结束后关闭文件；之后不能再开始事务。不隐式提交或同步。重复关闭无效果。
//
// 关闭会解除映射，事务借出的切片随即失效，所以必须等事务结束：每个事务持有 openMu 的读锁，
// 这里取写锁。读事务若不结束，close 会一直等待；持有事务的 goroutine 自己调用 close 会死锁。
func (d *db) close() error {
	d.openMu.Lock()
	defer d.openMu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.pager.Close()
}

// update 在一个写事务里执行 fn：fn 返回 nil 就提交，返回错误就回滚（什么也不写）。
// 提交失败时返回提交的错误；此前 fn 做的修改全部作废。
func (d *db) update(fn func(t *tx) error) error {
	t, err := d.begin(true)
	if err != nil {
		return err
	}
	if d.failed != nil { // 持有写者锁之后再看：failed 只由写事务读写
		t.rollback()
		return d.failed
	}
	if err := fn(t); err != nil {
		t.rollback()
		return err
	}
	return t.commit()
}

// view 在一个只读事务里执行 fn，看到开始时最近一次提交的版本。
// 提交失败（failed）之后仍可读：旧版本用到的页从未被改写，内容完整。
func (d *db) view(fn func(t *tx) error) error {
	t, err := d.begin(false)
	if err != nil {
		return err
	}
	defer t.rollback()
	return fn(t)
}

// fail 记录提交在写 meta 之后的失败，此后拒绝写事务。
func (d *db) fail(err error) {
	d.failed = fmt.Errorf("%w: %v", ErrDBFailed, err)
}
