package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/luckysxx/go-btree-kv/pager"
)

var errInjected = errors.New("injected fault")

// opLog 包装 db 的写页与同步，按调用顺序记录 "write <页号>"、"write meta <槽位>"、"sync"。
// 注入故障的方式（被选中的调用不执行真实操作，直接返回 errInjected）：
//   - failAt >= 0：第 failAt 次调用（从 0 数）
//   - failOn == "meta"：第一次 meta 写入
//   - failOn == "sync-after-meta"：meta 写入之后的第一次同步
//
// torn 为 true 且失败的是 meta 写入时，先真实写入一个撕裂的页（前 tornBytes 字节是新 meta，
// 其余仍是磁盘上的旧内容）再返回错误，模拟写 meta 途中断电。
// 同一个 db 只安装一次，用 reset 换一轮设置：重复安装会层层包装，旧设置可能在之后意外触发。
type opLog struct {
	ops     []string
	failAt  int
	failOn  string
	torn    bool
	sawMeta bool
}

const tornBytes = 20 // 撕裂位置：magic、version、pageSize、root 的前半是新的，其余是旧的

func installOpLog(d *db, failAt int) *opLog {
	log := &opLog{failAt: failAt}
	realWrite, realSync := d.writePage, d.sync
	d.writePage = func(id uint64, page []byte) error {
		isMeta := id < firstDataPage
		name := fmt.Sprintf("write %d", id)
		if isMeta {
			name = fmt.Sprintf("write meta %d", id)
		}
		if err := log.record(name); err != nil {
			if isMeta && log.torn {
				if tornErr := realWrite(id, tornPage(d, id, page)); tornErr != nil {
					return tornErr
				}
			}
			return err
		}
		return realWrite(id, page)
	}
	d.sync = func() error {
		if err := log.record("sync"); err != nil {
			return err
		}
		return realSync()
	}
	return log
}

// tornPage 返回页 id 的撕裂版本：前 tornBytes 字节取自 newPage，其余取磁盘上的现有内容。
func tornPage(d *db, id uint64, newPage []byte) []byte {
	torn := make([]byte, PageSize)
	if old, err := d.pager.ReadPage(id); err == nil {
		copy(torn, old)
	}
	copy(torn, newPage[:tornBytes])
	return torn
}

// metaWrites 返回 ops 中各次 meta 写入的槽位。
func (l *opLog) metaWrites() []string {
	var slots []string
	for _, op := range l.ops {
		if strings.HasPrefix(op, "write meta ") {
			slots = append(slots, strings.TrimPrefix(op, "write meta "))
		}
	}
	return slots
}

func (l *opLog) reset(failAt int, failOn string, torn bool) {
	l.ops, l.failAt, l.failOn, l.torn, l.sawMeta = nil, failAt, failOn, torn, false
}

func (l *opLog) record(name string) error {
	index := len(l.ops)
	l.ops = append(l.ops, name)
	isMeta := strings.HasPrefix(name, "write meta ")
	fail := index == l.failAt ||
		(l.failOn == "meta" && isMeta) ||
		(l.failOn == "sync-after-meta" && name == "sync" && l.sawMeta)
	if isMeta {
		l.sawMeta = true
	}
	if fail {
		l.failAt, l.failOn = -1, "" // 每种设置只触发一次
		return errInjected
	}
	return nil
}

func openTestDB(t *testing.T, path string) *db {
	t.Helper()
	d, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mustBegin 开始一个事务；调用者负责 rollback 或 commit。
func mustBegin(t *testing.T, d *db, writable bool) *tx {
	t.Helper()
	tx, err := d.begin(writable)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func reopenTestDB(t *testing.T, d *db, path string) *db {
	t.Helper()
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	return openTestDB(t, path)
}

func dbPut(t *testing.T, d *db, key, val string) {
	t.Helper()
	err := d.update(func(tx *tx) error { return tx.put([]byte(key), []byte(val)) })
	if err != nil {
		t.Fatal(err)
	}
}

// readValue 在读事务里查 key，返回 value 的副本（借用切片只在事务内有效）。
func readValue(t *testing.T, d *db, key string) (string, bool) {
	t.Helper()
	var value string
	var found bool
	err := d.view(func(tx *tx) error {
		v, ok, err := tx.get([]byte(key))
		value, found = string(v), ok
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return value, found
}

// checkDB 在读事务里做全树检查，并确认所有可达页都在高水位以下、文件覆盖高水位。
func checkDB(t *testing.T, d *db) int {
	t.Helper()
	var height int
	err := d.view(func(tx *tx) error {
		h, err := tx.tree.check()
		height = h
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.pager.FileSize() < int64(d.meta.highWater)*PageSize {
		t.Fatalf("文件 %d 字节，短于高水位 %d 页", d.pager.FileSize(), d.meta.highWater)
	}
	checkSpace(t, d)
	return height
}

// checkSpace 做页空间的账目检查：从已提交的根可达的页、空闲页、待回收页两两不重叠，
// 合起来恰好是 [firstDataPage, 高水位) 的全部页。
// 重叠说明一张在用的页会被重新分配（损坏）；有遗漏说明页泄漏。
func checkSpace(t *testing.T, d *db) {
	t.Helper()
	owner := make(map[uint64]string)
	claim := func(id uint64, who string) {
		if previous, taken := owner[id]; taken {
			t.Fatalf("页 %d 同时属于 %s 与 %s", id, previous, who)
		}
		if id < firstDataPage || id >= d.meta.highWater {
			t.Fatalf("页 %d（%s）不在 [%d, %d) 内", id, who, firstDataPage, d.meta.highWater)
		}
		owner[id] = who
	}
	reachable := make(map[uint64]bool)
	if err := d.markReachable(d.meta.root, reachable); err != nil {
		t.Fatal(err)
	}
	for id := range reachable {
		claim(id, "可达")
	}
	for _, id := range d.free {
		claim(id, "空闲")
	}
	for txid, ids := range d.pending {
		for _, id := range ids {
			claim(id, fmt.Sprintf("待回收（事务 %d）", txid))
		}
	}
	for id := uint64(firstDataPage); id < d.meta.highWater; id++ {
		if _, accounted := owner[id]; !accounted {
			t.Fatalf("页 %d 泄漏：不可达、不空闲、也不在待回收表中（高水位 %d）", id, d.meta.highWater)
		}
	}
}

// reclaimable 返回 db 中空闲页与待回收页的集合（重开后应全部变为空闲页）。
func reclaimable(d *db) map[uint64]bool {
	set := make(map[uint64]bool)
	for _, id := range d.free {
		set[id] = true
	}
	for _, ids := range d.pending {
		for _, id := range ids {
			set[id] = true
		}
	}
	return set
}

// scanAll 用游标读出整棵树（副本）。
func scanAll(t *testing.T, d *db) map[string]string {
	t.Helper()
	got := make(map[string]string)
	err := d.view(func(tx *tx) error {
		c := tx.tree.newCursor()
		if err := c.first(); err != nil {
			return err
		}
		for c.valid() {
			got[string(c.key())] = string(c.value())
			if err := c.next(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPageSizeMatchesPager(t *testing.T) {
	if PageSize != pager.PageSize {
		t.Fatalf("btree.PageSize = %d，pager.PageSize = %d", PageSize, pager.PageSize)
	}
}

func TestMetaEncodeDecode(t *testing.T) {
	m := meta{root: 7, highWater: 20, txid: 5}
	got, err := decodeMeta(encodeMeta(m), 1)
	if err != nil || got != m {
		t.Fatalf("decode = %+v, %v", got, err)
	}
	for _, bad := range []meta{{root: 1, highWater: 5, txid: 1}, {root: 5, highWater: 5, txid: 1}, {root: 0, highWater: 0, txid: 1}} {
		if _, err := decodeMeta(encodeMeta(bad), 1); !errors.Is(err, ErrInvalidDB) {
			t.Fatalf("%+v: err = %v，期望 ErrInvalidDB", bad, err)
		}
	}
	// txid 5 应在槽位 1：出现在槽位 0 说明写错了位置。
	if _, err := decodeMeta(encodeMeta(m), 0); !errors.Is(err, ErrInvalidDB) {
		t.Fatalf("错误槽位 err = %v", err)
	}
}

// 校验和覆盖 meta 的每个字节：任意一个字节被改动都能发现。
func TestMetaDetectsCorruption(t *testing.T) {
	page := encodeMeta(meta{root: 7, highWater: 20, txid: 5})
	for offset := 0; offset < offMetaChecksum+8; offset++ {
		corrupted := bytes.Clone(page)
		corrupted[offset] ^= 0x01
		if _, err := decodeMeta(corrupted, 1); err == nil {
			t.Fatalf("改动第 %d 字节没有被发现", offset)
		}
	}
	// 撕裂：magic 等开头字段来自新 meta，其余来自旧 meta。
	oldPage := encodeMeta(meta{root: 3, highWater: 10, txid: 3})
	newPage := encodeMeta(meta{root: 9, highWater: 30, txid: 5})
	torn := bytes.Clone(oldPage)
	copy(torn, newPage[:tornBytes])
	if _, err := decodeMeta(torn, 1); !errors.Is(err, ErrMetaChecksum) {
		t.Fatalf("撕裂的 meta err = %v，期望 ErrMetaChecksum", err)
	}
}

func TestPickMeta(t *testing.T) {
	older := meta{root: 2, highWater: 3, txid: 4}
	newer := meta{root: 5, highWater: 6, txid: 5}
	errBad := fmt.Errorf("%w: test", ErrMetaChecksum)
	errVersion := fmt.Errorf("%w: test", ErrVersionMismatch)
	cases := []struct {
		name    string
		m0      meta
		err0    error
		m1      meta
		err1    error
		want    meta
		wantErr error
	}{
		{"both-valid-slot1-newer", older, nil, newer, nil, newer, nil},
		{"both-valid-slot0-newer", meta{root: 5, highWater: 6, txid: 6}, nil, newer, nil, meta{root: 5, highWater: 6, txid: 6}, nil},
		{"only-slot0", older, nil, meta{}, errBad, older, nil},
		{"only-slot1", meta{}, errBad, newer, nil, newer, nil},
		{"none", meta{}, errBad, meta{}, errBad, meta{}, ErrInvalidDB},
		{"both-other-version", meta{}, errVersion, meta{}, errVersion, meta{}, ErrVersionMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickMeta(tc.m0, tc.err0, tc.m1, tc.err1)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v，期望 %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v；期望 %+v", got, err, tc.want)
			}
		})
	}
}

// 新文件：初始化走一次普通提交，得到空叶子根；关闭重开后状态相同。
func TestDBOpenNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	want := meta{root: firstDataPage, highWater: firstDataPage + 1, txid: 1}
	if d.meta != want {
		t.Fatalf("新库 meta = %+v，期望 %+v", d.meta, want)
	}
	if h := checkDB(t, d); h != 1 {
		t.Fatalf("空树高 = %d", h)
	}
	if _, found := readValue(t, d, "a"); found {
		t.Fatal("空库查到了数据")
	}
	d = reopenTestDB(t, d, path)
	defer d.close()
	if d.meta != want {
		t.Fatalf("重开后 meta = %+v", d.meta)
	}
}

// 提交顺序：全部数据页写完 → sync → 写 meta → sync，且写出的正是事务新建的页。
func TestDBCommitOrder(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	log := installOpLog(d, -1)
	before := d.meta.highWater
	// 一次提交里写入足够多的数据，让根叶子分裂，dirty 页不止一张。
	err := d.update(func(tx *tx) error {
		for i := 0; i < 40; i++ {
			if err := tx.put([]byte(fmt.Sprintf("k%03d", i)), bytes.Repeat([]byte{'v'}, 200)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	n := len(log.ops)
	if n < 5 {
		t.Fatalf("操作太少：%v", log.ops)
	}
	wantTail := []string{"sync", fmt.Sprintf("write meta %d", metaSlotFor(d.meta.txid)), "sync"}
	if !slices.Equal(log.ops[n-3:], wantTail) {
		t.Fatalf("结尾应为 %v，实际 %v", wantTail, log.ops)
	}
	var written []string
	for id := before; id < d.meta.highWater; id++ {
		written = append(written, fmt.Sprintf("write %d", id))
	}
	gotWritten := slices.Clone(log.ops[:n-3])
	slices.Sort(gotWritten)
	slices.Sort(written)
	if !slices.Equal(gotWritten, written) {
		t.Fatalf("第一次 sync 之前应恰好写出新分配的页 %v，实际 %v", written, log.ops[:n-3])
	}
	if h := checkDB(t, d); h < 2 {
		t.Fatalf("树高 = %d，期望根已分裂", h)
	}
}

// 没有任何修改的事务不写盘、不产生新版本。
func TestDBCommitWithoutChanges(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	dbPut(t, d, "a", "1")
	log := installOpLog(d, -1)
	before := d.meta
	err := d.update(func(tx *tx) error {
		_, err := tx.delete([]byte("missing"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(log.ops) != 0 || d.meta != before {
		t.Fatalf("无修改的提交产生了操作 %v 或改变了 meta", log.ops)
	}
}

// fn 返回错误：回滚，什么都不写，db 状态不变。
func TestDBUpdateErrorRollsBack(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	dbPut(t, d, "a", "1")
	log := installOpLog(d, -1)
	before := d.meta
	errStop := errors.New("stop")
	err := d.update(func(tx *tx) error {
		if err := tx.put([]byte("b"), []byte("2")); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("err = %v", err)
	}
	if len(log.ops) != 0 || d.meta != before {
		t.Fatalf("回滚产生了操作 %v 或改变了 meta", log.ops)
	}
	if _, found := readValue(t, d, "b"); found {
		t.Fatal("回滚的写入可见")
	}
}

// 在一次"只改根叶子"的提交里（写 1 页 → sync → 写 meta → sync），分别让第 0～3 次操作失败。
func TestDBCommitFailures(t *testing.T) {
	cases := []struct {
		name          string
		failAt        int
		torn          bool
		wantFailed    bool // 是否进入 ErrDBFailed
		wantAfterOpen bool // 进程内重开后能否看到新写入
	}{
		{"write-data", 0, false, false, false},
		{"first-sync", 1, false, false, false},
		{"write-meta", 2, false, true, false},
		// 写 meta 途中断电：新槽位撕裂、校验和不符，重开退回另一个槽位中的上一版。
		{"torn-meta", 2, true, true, false},
		// meta 已进入页缓存，只是 sync 失败；同一台机器上重开读到的是页缓存，所以看到新版本。
		// 真实断电时可能是旧的，也可能是新的——这正是内存状态无法判断、只能重开的原因。
		{"second-sync", 3, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kv.db")
			d := openTestDB(t, path)
			dbPut(t, d, "old", "1")
			before := d.meta
			log := installOpLog(d, tc.failAt)
			log.torn = tc.torn

			err := d.update(func(tx *tx) error { return tx.put([]byte("new"), []byte("2")) })
			if !errors.Is(err, errInjected) {
				t.Fatalf("err = %v，期望注入的错误；操作 %v", err, log.ops)
			}
			if d.meta != before {
				t.Fatalf("失败的提交改变了内存 meta：%+v → %+v", before, d.meta)
			}
			if gotFailed := d.failed != nil; gotFailed != tc.wantFailed {
				t.Fatalf("failed = %v，期望 %v", d.failed, tc.wantFailed)
			}
			if _, found := readValue(t, d, "new"); found {
				t.Fatal("失败的提交在内存中可见")
			}

			if tc.wantFailed {
				// 写 meta 之后失败：拒绝写事务，读仍可用（旧版本的页完好）。
				err := d.update(func(tx *tx) error { return tx.put([]byte("x"), []byte("y")) })
				if !errors.Is(err, ErrDBFailed) {
					t.Fatalf("失败后的写事务 err = %v", err)
				}
				if v, _ := readValue(t, d, "old"); v != "1" {
					t.Fatal("失败后读不到旧数据")
				}
			} else {
				// 写 meta 之前失败：相当于回滚。下一个事务分配到与失败事务相同的页号
				// （同一份空闲清单、同一高水位），覆盖失败事务写过的页。
				failedFirstWrite := log.ops[0]
				log.reset(-1, "", false)
				dbPut(t, d, "next", "3")
				if log.ops[0] != failedFirstWrite {
					t.Fatalf("下一次提交应重用失败事务的页号（%s），操作 %v", failedFirstWrite, log.ops)
				}
			}

			d = reopenTestDB(t, d, path)
			defer d.close()
			checkDB(t, d)
			if v, _ := readValue(t, d, "old"); v != "1" {
				t.Fatal("重开后旧数据丢失")
			}
			if _, found := readValue(t, d, "new"); found != tc.wantAfterOpen {
				t.Fatalf("重开后 new 可见 = %v，期望 %v", found, tc.wantAfterOpen)
			}
		})
	}
}

// 模拟"新页写进文件、meta 未写"时进程崩溃：文件比高水位长，重开只相信 meta。
func TestDBReopenIgnoresPagesAboveHighWater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	dbPut(t, d, "old", "1")
	before := d.meta
	installOpLog(d, 1) // 数据页真实写入文件，第一次 sync 失败，meta 未写
	err := d.update(func(tx *tx) error {
		for i := 0; i < 200; i++ {
			if err := tx.put([]byte(fmt.Sprintf("k%03d", i)), bytes.Repeat([]byte{'v'}, 100)); err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	if d.pager.FileSize() <= int64(before.highWater)*PageSize {
		t.Fatal("测试前提不成立：失败事务应已把页写到高水位之上")
	}

	d = reopenTestDB(t, d, path)
	defer d.close()
	if d.meta != before {
		t.Fatalf("重开后 meta = %+v，期望旧的 %+v", d.meta, before)
	}
	if got := scanAll(t, d); len(got) != 1 || got["old"] != "1" {
		t.Fatalf("重开后内容 = %v", got)
	}
	dbPut(t, d, "after", "2") // 覆盖高水位之上的旧内容
	checkDB(t, d)
}

// 读事务是快照：开始后再提交的修改看不到。value 借用只读映射，写它会触发错误。
func TestDBViewSnapshotAndReadOnlyValue(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	dbPut(t, d, "k", "old")

	reader := mustBegin(t, d, false)
	defer reader.rollback()
	dbPut(t, d, "k", "new")
	v, found, err := reader.get([]byte("k"))
	if err != nil || !found || string(v) != "old" {
		t.Fatalf("快照读到 %q, %v, %v，期望 old", v, found, err)
	}
	if got, _ := readValue(t, d, "k"); got != "new" {
		t.Fatalf("新事务读到 %q", got)
	}

	faulted := func() (faulted bool) {
		old := debug.SetPanicOnFault(true)
		defer debug.SetPanicOnFault(old)
		defer func() {
			if recover() != nil {
				faulted = true
			}
		}()
		v[0] = 'X'
		return false
	}()
	if !faulted {
		t.Fatal("写借出的 value 没有触发错误")
	}
}

func TestDBTxStateErrors(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.view(func(tx *tx) error { return tx.put([]byte("a"), []byte("1")) })
	if !errors.Is(err, ErrTxReadOnly) {
		t.Fatalf("读事务写入 err = %v", err)
	}
	var leaked *tx
	if err := d.update(func(tx *tx) error { leaked = tx; return tx.put([]byte("a"), []byte("1")) }); err != nil {
		t.Fatal(err)
	}
	if err := leaked.put([]byte("b"), []byte("2")); !errors.Is(err, ErrTxClosed) {
		t.Fatalf("已提交事务写入 err = %v", err)
	}
	if _, _, err := leaked.get([]byte("a")); !errors.Is(err, ErrTxClosed) {
		t.Fatalf("已提交事务读取 err = %v", err)
	}
	if err := leaked.commit(); !errors.Is(err, ErrTxClosed) {
		t.Fatalf("重复提交 err = %v", err)
	}
}

// Open 拒绝不可用的文件，且不修改它们。
func TestDBOpenRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "valid.db")
	d := openTestDB(t, valid)
	for i := 0; i < 300; i++ {
		dbPut(t, d, fmt.Sprintf("k%03d", i), string(bytes.Repeat([]byte{'v'}, 100)))
	}
	highWater := d.meta.highWater
	d.close()
	validData, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if highWater <= 4 {
		t.Fatalf("测试前提：高水位 %d 应超过 4 页", highWater)
	}

	// 两个 meta 槽位都要改：只改一个时，另一个仍有效，打开会成功（见 TestDBCorruptNewestMetaFallsBack）。
	versionData := bytes.Clone(validData)
	pageSizeData := bytes.Clone(validData)
	for slot := 0; slot < metaSlots; slot++ {
		binary.LittleEndian.PutUint32(versionData[slot*PageSize+offMetaVersion:], metaVersion+1)
		binary.LittleEndian.PutUint32(pageSizeData[slot*PageSize+offMetaPageSize:], 8192)
	}

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"foreign", bytes.Repeat([]byte{0xab}, 2*PageSize), ErrInvalidDB},
		{"short", []byte("hello"), ErrInvalidDB},
		{"zero-meta", make([]byte, 4*PageSize), ErrInvalidDB},
		{"version", versionData, ErrVersionMismatch},
		{"page-size", pageSizeData, ErrInvalidDB},
		{"truncated", validData[:4*PageSize], ErrInvalidDB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".db")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := openDB(path); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Fatal("打开失败时修改了文件")
			}
		})
	}
}

// 随机事务（每个含多次 put/delete，部分故意回滚），每次提交后全树检查，
// 定期关闭重开，与参考 map 对照。
func TestDBRandomTransactionsWithReopen(t *testing.T) {
	rng := rand.New(rand.NewSource(2026_09_29))
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	defer func() { d.close() }()
	model := make(map[string]string)
	errRollback := errors.New("rollback")

	for txIndex := 0; txIndex < 120; txIndex++ {
		pending := make(map[string]*string) // 本事务的修改；nil 表示删除
		rollback := rng.Intn(8) == 0
		err := d.update(func(tx *tx) error {
			ops := 1 + rng.Intn(60)
			for i := 0; i < ops; i++ {
				key := fmt.Sprintf("key-%04d", rng.Intn(600))
				if rng.Intn(3) == 0 {
					if _, err := tx.delete([]byte(key)); err != nil {
						return err
					}
					pending[key] = nil
					continue
				}
				val := string(bytes.Repeat([]byte{byte('a' + rng.Intn(26))}, 1+rng.Intn(300)))
				if err := tx.put([]byte(key), []byte(val)); err != nil {
					return err
				}
				pending[key] = &val
			}
			if rollback {
				return errRollback
			}
			return nil
		})
		if rollback {
			if !errors.Is(err, errRollback) {
				t.Fatalf("tx %d: err = %v", txIndex, err)
			}
		} else {
			if err != nil {
				t.Fatalf("tx %d: %v", txIndex, err)
			}
			for key, val := range pending {
				if val == nil {
					delete(model, key)
				} else {
					model[key] = *val
				}
			}
		}

		checkDB(t, d)
		if txIndex%15 == 14 {
			d = reopenTestDB(t, d, path)
		}
		got := scanAll(t, d)
		if len(got) != len(model) {
			t.Fatalf("tx %d: 树 %d 条，模型 %d 条", txIndex, len(got), len(model))
		}
		for key, val := range model {
			if got[key] != val {
				t.Fatalf("tx %d: key %s 不一致", txIndex, key)
			}
		}
	}
}

// 每次提交写另一个槽位：init 用 txid 1 写页 1，之后 0、1、0、1 轮流。
func TestDBAlternatesMetaSlots(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	log := installOpLog(d, -1)
	for i := 0; i < 4; i++ {
		dbPut(t, d, fmt.Sprintf("k%d", i), "v")
	}
	if got := log.metaWrites(); !slices.Equal(got, []string{"0", "1", "0", "1"}) {
		t.Fatalf("meta 写入槽位 = %v", got)
	}
	if d.meta.txid != 5 {
		t.Fatalf("txid = %d，期望 5", d.meta.txid)
	}
}

// corruptMetaSlot 关闭后直接改文件：把槽位 slot 的 root 字段改掉一个字节（校验和随之不符）。
func corruptMetaSlot(t *testing.T, path string, slot uint64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 1)
	offset := int64(slot)*PageSize + offMetaRoot
	if _, err := f.ReadAt(b, offset); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b, offset); err != nil {
		t.Fatal(err)
	}
}

// 最新的 meta 损坏：打开时退回另一个槽位中的上一版；之后的提交覆盖坏的槽位，一切照常。
func TestDBCorruptNewestMetaFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	dbPut(t, d, "a", "1")
	previous := d.meta
	dbPut(t, d, "b", "2")
	newest := d.meta
	d.close()

	corruptMetaSlot(t, path, metaSlotFor(newest.txid))
	d = openTestDB(t, path)
	defer func() { d.close() }()
	if d.meta != previous {
		t.Fatalf("打开后 meta = %+v，期望退回上一版 %+v", d.meta, previous)
	}
	if got := scanAll(t, d); len(got) != 1 || got["a"] != "1" {
		t.Fatalf("退回后的内容 = %v", got)
	}

	dbPut(t, d, "c", "3") // txid 与刚损坏的那份相同，写的正是坏槽位
	if d.meta.txid != newest.txid {
		t.Fatalf("txid = %d，期望重用 %d", d.meta.txid, newest.txid)
	}
	d = reopenTestDB(t, d, path)
	if got := scanAll(t, d); len(got) != 2 || got["c"] != "3" {
		t.Fatalf("重开后内容 = %v", got)
	}
	checkDB(t, d)
}

// 两个槽位都损坏：拒绝打开，且不修改文件。
func TestDBBothMetasCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	dbPut(t, d, "a", "1")
	d.close()
	corruptMetaSlot(t, path, 0)
	corruptMetaSlot(t, path, 1)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDB(path); !errors.Is(err, ErrInvalidDB) {
		t.Fatalf("err = %v，期望 ErrInvalidDB", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("打开失败时修改了文件")
	}
}

// 随机故障下双 meta 的不变量：随机事务在随机一步失败（含撕裂的 meta 写入），失败后关闭重开。
// 重开后的内容必须是最后一次确认成功的版本，或者（失败发生在写 meta 之后时）那次失败提交的新版本；
// 两个槽位不会同时无效（否则打不开）；之后继续提交仍然成立。
func TestDBRandomFaultsKeepMetaInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(2026_09_30))
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	defer func() { d.close() }()
	confirmed := make(map[string]string) // 最后一次确认成功的内容
	confirmedTxid := d.meta.txid
	log := installOpLog(d, -1)
	outcomes := make(map[string]int)

	for round := 0; round < 300; round++ {
		pending := maps.Clone(confirmed)
		switch rng.Intn(6) {
		case 0, 1, 2:
			log.reset(-1, "", false) // 不注入
		case 3:
			log.reset(rng.Intn(12), "", rng.Intn(2) == 0) // 任意一步，也可能超过本次操作数而不触发
		case 4:
			log.reset(-1, "meta", rng.Intn(2) == 0) // 写 meta 时失败，一半是撕裂写
		case 5:
			log.reset(-1, "sync-after-meta", false) // meta 已进页缓存，第二次同步失败
		}
		err := d.update(func(tx *tx) error {
			for i := 0; i < 1+rng.Intn(20); i++ {
				key := fmt.Sprintf("key-%03d", rng.Intn(200))
				if rng.Intn(4) == 0 {
					if _, err := tx.delete([]byte(key)); err != nil {
						return err
					}
					delete(pending, key)
					continue
				}
				// value 加长到一百多字节：200 个 key 需要多张叶子，树有两层以上，
				// 一个事务里会有多次页分配——过早重用旧页的错误才有机会暴露。
				val := fmt.Sprintf("r%d-%d-%s", round, i, strings.Repeat("x", 100+rng.Intn(100)))
				if err := tx.put([]byte(key), []byte(val)); err != nil {
					return err
				}
				pending[key] = val
			}
			return nil
		})
		if err == nil {
			confirmed = pending
			confirmedTxid = d.meta.txid
			continue
		}
		if !errors.Is(err, errInjected) {
			t.Fatalf("round %d: %v", round, err)
		}

		failedOps := log.ops
		metaStarted := slices.ContainsFunc(failedOps, func(op string) bool { return strings.HasPrefix(op, "write meta") })
		retry := d.update(func(tx *tx) error { return tx.put([]byte("retry"), []byte("x")) })
		if metaStarted && !errors.Is(retry, ErrDBFailed) {
			t.Fatalf("round %d: 写 meta 之后失败，下一个写事务 err = %v，期望 ErrDBFailed", round, retry)
		}
		if !metaStarted {
			// 写 meta 之前失败等于回滚：可以继续写。撤销这次试写，保持 confirmed 不变。
			if retry != nil {
				t.Fatalf("round %d: 写 meta 之前失败后，下一个写事务 err = %v", round, retry)
			}
			log.reset(-1, "", false)
			if err := d.update(func(tx *tx) error { _, err := tx.delete([]byte("retry")); return err }); err != nil {
				t.Fatal(err)
			}
			confirmedTxid = d.meta.txid
		}
		d = reopenTestDB(t, d, path) // 打不开即说明两个槽位同时无效
		log = installOpLog(d, -1)
		checkDB(t, d)
		got := scanAll(t, d)
		switch {
		case maps.Equal(got, confirmed) && !maps.Equal(got, pending):
			if d.meta.txid != confirmedTxid {
				t.Fatalf("round %d: 内容是确认版本，txid = %d，期望 %d", round, d.meta.txid, confirmedTxid)
			}
			outcomes["old"]++
		case maps.Equal(got, pending) && !maps.Equal(got, confirmed):
			if d.meta.txid != confirmedTxid+1 {
				t.Fatalf("round %d: 内容是新版本，txid = %d，期望 %d", round, d.meta.txid, confirmedTxid+1)
			}
			confirmed, confirmedTxid = pending, d.meta.txid
			outcomes["new"]++
		case maps.Equal(got, confirmed): // 本轮修改恰好没有改变内容，无法区分
			confirmedTxid = d.meta.txid
		default:
			t.Fatalf("round %d: 重开后内容既不是确认版本也不是新版本（操作 %v）", round, failedOps)
		}
		if failedOps[len(failedOps)-1] != "sync" && strings.HasPrefix(failedOps[len(failedOps)-1], "write meta") {
			outcomes["meta-write-failed"]++
		}
	}
	// 覆盖检查：两种结局与写 meta 失败都要真的发生过，否则这个测试没有测到它要测的东西。
	for _, want := range []string{"old", "new", "meta-write-failed"} {
		if outcomes[want] == 0 {
			t.Fatalf("随机故障没有覆盖到 %q：%v", want, outcomes)
		}
	}
	t.Logf("结局统计：%v", outcomes)
}

// reachablePages 返回从事务当前的根可达的页数（做一次全树检查）。
func reachablePages(t *testing.T, tx *tx) int {
	t.Helper()
	checker := treeChecker{tree: tx.tree, seen: make(map[uint64]bool)}
	if _, err := checker.walk(tx.tree.root, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	return len(checker.seen)
}

// replace 的两种情况：已提交的页必须复制到新页号；本事务新建的页复用页号。
func TestTxPageStoreReplace(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	dbPut(t, d, "a", "1")
	committedRoot := d.meta.root

	writer := mustBegin(t, d, true)
	defer writer.rollback()
	store := txPageStore{writer}
	committedPage, err := store.get(committedRoot)
	if err != nil {
		t.Fatal(err)
	}

	// 已提交的页：分配新页号，原页不进 dirty。
	versionOne := bytes.Repeat([]byte{1}, PageSize)
	newID, err := store.replace(committedRoot, versionOne)
	if err != nil {
		t.Fatal(err)
	}
	if _, inDirty := writer.dirty[newID]; newID == committedRoot || !inDirty {
		t.Fatalf("替换已提交的页 %d 得到页号 %d，应分配另一个页号并放入 dirty", committedRoot, newID)
	}
	if !slices.Contains(writer.freed, committedRoot) {
		t.Fatalf("被替换的已提交页 %d 应记入释放列表 %v", committedRoot, writer.freed)
	}
	if _, inDirty := writer.dirty[committedRoot]; inDirty {
		t.Fatal("已提交的页不应进入 dirty（提交时会覆盖文件里的旧版本）")
	}
	if got, _ := store.get(committedRoot); !bytes.Equal(got, committedPage) {
		t.Fatal("已提交的页内容被改变")
	}

	// 本事务新建的页：复用页号，不再分配。
	borrowed, err := store.get(newID)
	if err != nil {
		t.Fatal(err)
	}
	highWater := writer.meta.highWater
	versionTwo := bytes.Repeat([]byte{2}, PageSize)
	sameID, err := store.replace(newID, versionTwo)
	if err != nil {
		t.Fatal(err)
	}
	if sameID != newID || writer.meta.highWater != highWater || len(writer.dirty) != 1 || len(writer.freed) != 1 {
		t.Fatalf("替换自己新建的页 %d 得到页号 %d，高水位 %d → %d，dirty %d 页，释放列表 %v；应复用页号且不释放任何页",
			newID, sameID, highWater, writer.meta.highWater, len(writer.dirty), writer.freed)
	}
	if got, _ := store.get(newID); !bytes.Equal(got, versionTwo) {
		t.Fatal("替换后读到的不是新内容")
	}
	// 复用的是页号，不是字节：之前借出的切片仍是旧内容；放进 dirty 的是副本。
	if !bytes.Equal(borrowed, versionOne) {
		t.Fatal("替换改动了之前借出的切片")
	}
	versionTwo[0] = 9
	if got, _ := store.get(newID); got[0] != 2 {
		t.Fatal("dirty 中的页与调用者的缓冲共享了底层数组")
	}

	// 与 save 相同的检查。
	if _, err := store.replace(newID, make([]byte, PageSize-1)); !errors.Is(err, ErrBadPageSize) {
		t.Fatalf("页长错误 err = %v", err)
	}
	reader := mustBegin(t, d, false)
	defer reader.rollback()
	if _, err := (txPageStore{reader}).replace(committedRoot, versionOne); !errors.Is(err, ErrTxReadOnly) {
		t.Fatalf("只读事务 replace err = %v", err)
	}
}

// 一个事务里只做 put：写出的页恰好是新树中由本事务新建的页，没有过时页；
// 高水位只增加这么多。批次越大，这个性质越重要。
func TestDBBatchWritesOnlyReachablePages(t *testing.T) {
	for _, count := range []int{10, 100, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
			defer d.close()
			before := d.meta.highWater
			var written, reachable int
			err := d.update(func(tx *tx) error {
				for i := 0; i < count; i++ {
					if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), make([]byte, 50)); err != nil {
						return err
					}
				}
				written = len(tx.dirty)
				reachable = reachablePages(t, tx)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// 库是新建的，旧树只有一张空叶子且已被替换，所以新树的每一页都由本事务新建。
			if written != reachable {
				t.Fatalf("%d 次 put：写出 %d 页，新树可达 %d 页，过时页 %d", count, written, reachable, written-reachable)
			}
			if grew := int(d.meta.highWater - before); grew != written {
				t.Fatalf("高水位增加 %d，写出 %d 页", grew, written)
			}
			checkDB(t, d)
			t.Logf("%d 次 put：写出 %d 页，全部可达", count, written)
		})
	}
}

// 第二个事务只改一条记录：只复制从根到那片叶子的路径，其余页不动。
func TestDBSecondTxCopiesOnlyTouchedPath(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.update(func(tx *tx) error {
		for i := 0; i < 1000; i++ {
			if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), make([]byte, 50)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	height := checkDB(t, d)

	var written int
	err = d.update(func(tx *tx) error {
		for i := 0; i < 20; i++ { // 同一条记录改 20 次
			if err := tx.put([]byte("key-00500"), []byte(fmt.Sprint(i))); err != nil {
				return err
			}
		}
		written = len(tx.dirty)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != height {
		t.Fatalf("同一条记录改 20 次：写出 %d 页，期望等于树高 %d（一条路径）", written, height)
	}
	if v, _ := readValue(t, d, "key-00500"); v != "19" {
		t.Fatalf("读到 %q", v)
	}
	checkDB(t, d)
}

// 删除与根收缩摘掉的页，如果是本事务新建的，立即回到事务的空闲清单：不写盘，也能被后面的分配重用。
// 所以即使大量删除，写出的页仍恰好是新树中由本事务新建的页。
func TestDBBatchDeleteWritesNoStalePages(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	const count = 1000
	var written, reachable, peak int
	err := d.update(func(tx *tx) error {
		for i := 0; i < count; i++ {
			if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), make([]byte, 50)); err != nil {
				return err
			}
		}
		peak = reachablePages(t, tx)
		for i := 0; i < count-10; i++ { // 删到只剩 10 条
			if _, err := tx.delete([]byte(fmt.Sprintf("key-%05d", i))); err != nil {
				return err
			}
		}
		written = len(tx.dirty)
		reachable = reachablePages(t, tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != reachable {
		t.Fatalf("写出 %d 页，可达 %d 页，过时 %d 页", written, reachable, written-reachable)
	}
	// 被摘掉的页号留在空闲清单里：高水位以下、不可达，下一个事务可以直接重用。
	if len(d.free) != peak-reachable {
		t.Fatalf("空闲页 %d，期望 %d（删除前 %d 页 − 现在可达 %d 页）", len(d.free), peak-reachable, peak, reachable)
	}
	checkDB(t, d)
	t.Logf("1000 次 put 后删除 990 条：写出 %d 页，全部可达；%d 个页号回到空闲清单", written, len(d.free))
}

// 事务里先读出一个 value，再修改同一片叶子：之前借出的 value 内容不变。
func TestDBTxBorrowedValueSurvivesLaterPut(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.update(func(tx *tx) error {
		if err := tx.put([]byte("a"), []byte("old-a")); err != nil {
			return err
		}
		borrowed, found, err := tx.get([]byte("a"))
		if err != nil || !found {
			return fmt.Errorf("get a: %v %v", found, err)
		}
		// 同一片叶子（此时整棵树只有一页）再改两次，其中一次覆盖 a 本身。
		if err := tx.put([]byte("b"), []byte("new-b")); err != nil {
			return err
		}
		if err := tx.put([]byte("a"), []byte("NEW-A")); err != nil {
			return err
		}
		if string(borrowed) != "old-a" {
			return fmt.Errorf("之前借出的 value 变成了 %q", borrowed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := readValue(t, d, "a"); v != "NEW-A" {
		t.Fatalf("提交后读到 %q", v)
	}
}

// failingStore 包装页存储，在第 failAt 次 replace（从 0 数）时返回错误，模拟操作中途出错。
type failingStore struct {
	pageStore
	replaces int
	failAt   int
}

func (s *failingStore) replace(oldID uint64, page []byte) (uint64, error) {
	index := s.replaces
	s.replaces++
	if index == s.failAt {
		return 0, errInjected
	}
	return s.pageStore.replace(oldID, page)
}

// 操作中途出错（叶子已替换、父页还没更新）后，事务只能回滚：后续操作与提交都被拒绝，db 不变。
func TestDBTxFailsAfterOperationError(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.update(func(tx *tx) error {
		for i := 0; i < 300; i++ { // 足够多，树高至少为 2
			if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), make([]byte, 50)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if height := checkDB(t, d); height < 2 {
		t.Fatalf("测试前提：树高 %d 应至少为 2", height)
	}
	before := d.meta
	log := installOpLog(d, -1)

	var swallowed error
	err = d.update(func(tx *tx) error {
		tx.tree.store = &failingStore{pageStore: tx.tree.store, failAt: 1} // 叶子替换成功，父页替换失败
		swallowed = tx.put([]byte("key-00100"), []byte("x"))
		if err := tx.put([]byte("other"), []byte("y")); !errors.Is(err, ErrTxFailed) {
			return fmt.Errorf("出错后的 put err = %v，期望 ErrTxFailed", err)
		}
		if _, _, err := tx.get([]byte("key-00100")); !errors.Is(err, ErrTxFailed) {
			return fmt.Errorf("出错后的 get err = %v，期望 ErrTxFailed", err)
		}
		return nil // 故意吞掉错误：提交必须拒绝
	})
	if !errors.Is(swallowed, errInjected) {
		t.Fatalf("注入的错误没有出现：%v", swallowed)
	}
	if !errors.Is(err, ErrTxFailed) {
		t.Fatalf("失败事务的提交 err = %v，期望 ErrTxFailed", err)
	}
	if len(log.ops) != 0 || d.meta != before || d.failed != nil {
		t.Fatalf("失败事务写了盘或改变了 db：ops %v, meta %+v, failed %v", log.ops, d.meta, d.failed)
	}
	if v, _ := readValue(t, d, "key-00100"); v != string(make([]byte, 50)) {
		t.Fatal("失败事务的修改可见")
	}
	dbPut(t, d, "after", "ok") // db 本身没有失败，可以继续使用
	checkDB(t, d)
}

// 输入被拒绝（key 过长、记录过大）时还没有替换任何页，事务完好，可以继续并提交。
func TestDBTxInputErrorsDoNotFailTx(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.update(func(tx *tx) error {
		if err := tx.put([]byte("a"), []byte("1")); err != nil {
			return err
		}
		if err := tx.put(make([]byte, maxKeySize+1), []byte("v")); !errors.Is(err, ErrKeyTooLarge) {
			return fmt.Errorf("过长的 key err = %v", err)
		}
		if err := tx.put([]byte("big"), make([]byte, PageSize)); !errors.Is(err, ErrRecordTooLarge) {
			return fmt.Errorf("过大的记录 err = %v", err)
		}
		return tx.put([]byte("b"), []byte("2"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := scanAll(t, d); len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("内容 = %v", got)
	}
	checkDB(t, d)
}

// releasePending 的规则：pending[T] 在没有读者、或 T 不大于最小读者版本号时放出。
func TestReleasePending(t *testing.T) {
	cases := []struct {
		name        string
		readers     map[uint64]int
		wantFree    []uint64 // 放出后空闲清单中的页号（不计顺序）
		wantPending []uint64 // 仍留在待回收表中的事务号
	}{
		{"没有读者：全部放出", nil, []uint64{1, 10, 11, 12, 13}, nil},
		{"读者在版本 5：放出事务 3 与 5 的", map[uint64]int{5: 1}, []uint64{1, 10, 11, 12}, []uint64{7}},
		{"读者在版本 4 和 9：按最小的 4 算", map[uint64]int{4: 1, 9: 2}, []uint64{1, 10}, []uint64{5, 7}},
		{"读者在版本 7：三个都放出", map[uint64]int{7: 1}, []uint64{1, 10, 11, 12, 13}, nil},
		{"读者在版本 2：都要留着", map[uint64]int{2: 3}, []uint64{1}, []uint64{3, 5, 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &db{
				free:    []uint64{1},
				pending: map[uint64][]uint64{3: {10}, 5: {11, 12}, 7: {13}},
				readers: tc.readers,
			}
			d.releasePending()
			gotFree := slices.Clone(d.free)
			slices.Sort(gotFree)
			if !slices.Equal(gotFree, tc.wantFree) {
				t.Errorf("空闲清单 = %v，期望 %v", gotFree, tc.wantFree)
			}
			var gotPending []uint64
			for txid := range d.pending {
				gotPending = append(gotPending, txid)
			}
			slices.Sort(gotPending)
			if !slices.Equal(gotPending, tc.wantPending) {
				t.Errorf("待回收表剩余事务 = %v，期望 %v", gotPending, tc.wantPending)
			}
		})
	}
}

// 反复修改同一条记录：旧版本的页被后面的事务重用，高水位（文件）不再增长。
func TestDBReusesFreedPages(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	for i := 0; i < 5; i++ { // 先提交几次，进入稳定状态
		dbPut(t, d, "k", fmt.Sprint(i))
	}
	stable := d.meta.highWater
	for i := 0; i < 200; i++ {
		dbPut(t, d, "k", fmt.Sprint(i))
		checkDB(t, d)
	}
	if d.meta.highWater != stable {
		t.Fatalf("再提交 200 次后高水位 %d → %d：旧版本的页没有被重用", stable, d.meta.highWater)
	}
	if v, _ := readValue(t, d, "k"); v != "199" {
		t.Fatalf("读到 %q", v)
	}
}

// 读事务打开期间，它那个版本用到的页不会被重用：内容始终不变，文件因此增长。
// 读事务结束后，积压的页被放出，重用恢复。
func TestDBReaderPinsPages(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	err := d.update(func(tx *tx) error {
		for i := 0; i < 300; i++ {
			if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), []byte("v0")); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dbPut(t, d, "key-00000", "v0") // 再提交一次：读者打开时，待回收表里已有它这个版本的事务释放的页
	snapshot := scanAll(t, d)

	reader := mustBegin(t, d, false)
	readerPages := make(map[uint64]bool)
	if err := d.markReachable(reader.meta.root, readerPages); err != nil {
		t.Fatal(err)
	}
	opened := d.meta.highWater

	for round := 1; round <= 60; round++ {
		err := d.update(func(tx *tx) error {
			for i := 0; i < 300; i += 7 {
				if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), []byte(fmt.Sprintf("v%d", round))); err != nil {
					return err
				}
			}
			for id := range tx.dirty { // 写事务新建的页不能占用读者还在看的页号
				if readerPages[id] {
					return fmt.Errorf("第 %d 轮：页 %d 仍被读事务使用，却被重新分配", round, id)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		checkDB(t, d)
		// 读者看到的仍是打开时的完整版本。
		if _, err := reader.tree.check(); err != nil {
			t.Fatalf("第 %d 轮：读事务的树损坏：%v", round, err)
		}
		c := reader.tree.newCursor()
		if err := c.first(); err != nil {
			t.Fatal(err)
		}
		seen := 0
		for c.valid() {
			if snapshot[string(c.key())] != string(c.value()) {
				t.Fatalf("第 %d 轮：读事务看到 %s = %q，打开时是 %q", round, c.key(), c.value(), snapshot[string(c.key())])
			}
			seen++
			if err := c.next(); err != nil {
				t.Fatal(err)
			}
		}
		if seen != len(snapshot) {
			t.Fatalf("第 %d 轮：读事务看到 %d 条，打开时 %d 条", round, seen, len(snapshot))
		}
	}
	pinned := d.meta.highWater
	if pinned <= opened {
		t.Fatalf("测试前提：读事务打开期间高水位应增长（%d → %d）", opened, pinned)
	}

	reader.rollback() // 读事务结束：积压的页可以放出
	for round := 0; round < 60; round++ {
		err := d.update(func(tx *tx) error {
			for i := 0; i < 300; i += 7 {
				if err := tx.put([]byte(fmt.Sprintf("key-%05d", i)), []byte("after")); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		checkDB(t, d)
	}
	if d.meta.highWater != pinned {
		t.Fatalf("读事务结束后高水位仍在增长：%d → %d", pinned, d.meta.highWater)
	}
	t.Logf("高水位：读事务打开时 %d，打开期间涨到 %d，结束后保持 %d", opened, pinned, d.meta.highWater)
}

// 关闭重开后，空闲清单由"高水位以下不可达的页"重算：等于重开前的空闲页加待回收页，
// 且立即全部可用。
func TestDBRebuildFreeOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	d := openTestDB(t, path)
	defer func() { d.close() }()
	for round := 0; round < 30; round++ {
		err := d.update(func(tx *tx) error {
			for i := 0; i < 50; i++ {
				key := []byte(fmt.Sprintf("key-%03d", (round*13+i)%200))
				if i%5 == 0 {
					if _, err := tx.delete(key); err != nil {
						return err
					}
				} else if err := tx.put(key, bytes.Repeat([]byte{'v'}, 100)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkDB(t, d)
	before := reclaimable(d)
	highWater := d.meta.highWater
	if len(before) == 0 {
		t.Fatal("测试前提：应有可回收的页")
	}

	d = reopenTestDB(t, d, path)
	checkDB(t, d)
	if len(d.pending) != 0 {
		t.Fatalf("重开后待回收表应为空：%v", d.pending)
	}
	if after := reclaimable(d); !maps.Equal(after, before) {
		t.Fatalf("重开后空闲页 %d 个，重开前空闲加待回收 %d 个", len(after), len(before))
	}
	dbPut(t, d, "after-reopen", "ok") // 重算出的空闲页可以直接使用
	if d.meta.highWater != highWater {
		t.Fatalf("重开后的提交没有重用空闲页：高水位 %d → %d", highWater, d.meta.highWater)
	}
	checkDB(t, d)
}

// 回滚的事务从空闲清单里取过页号、也打算释放一些页：回滚后 db 的空闲清单与待回收表都不变。
func TestDBRollbackKeepsFreeList(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	for i := 0; i < 5; i++ {
		dbPut(t, d, "k", fmt.Sprint(i))
	}
	dbPut(t, d, "other", "x")
	errStop := errors.New("stop")
	var used int
	err := d.update(func(tx *tx) error {
		freeAtStart := len(tx.free)
		if err := tx.put([]byte("k"), []byte("rolled back")); err != nil {
			return err
		}
		if _, err := tx.delete([]byte("other")); err != nil {
			return err
		}
		used = freeAtStart - len(tx.free)
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("err = %v", err)
	}
	if used == 0 {
		t.Fatal("测试前提：回滚的事务应从空闲清单取过页号")
	}
	// update 开始时已把可放出的待回收页移入 db.free，这一步不随回滚撤销（也不需要）；
	// 回滚不能丢失或多出任何页号：账目检查通过即可。
	checkDB(t, d)
	if v, _ := readValue(t, d, "k"); v != "4" {
		t.Fatalf("回滚后读到 %q", v)
	}
	highWater := d.meta.highWater
	dbPut(t, d, "k", "next")
	if d.meta.highWater != highWater {
		t.Fatalf("回滚后空闲页丢失：下一次提交把高水位从 %d 推到 %d", highWater, d.meta.highWater)
	}
	checkDB(t, d)
}
