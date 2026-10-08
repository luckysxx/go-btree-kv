package btree

// 并发测试（E8）：一个写者加多个读者。这些测试只在竞争检测下运行（go test -race）：
// 缺少锁时，不带竞争检测的运行可能偶然通过，也可能让整个测试进程崩溃。
// 为了跑得快，写者的同步被换成空操作——这里检验的是并发，不是持久性。

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func requireRace(t *testing.T) {
	t.Helper()
	if !raceEnabled {
		t.Skip("并发测试需要竞争检测：go test -race")
	}
}

// roundValue 是第 round 轮写入的 value：轮次加上填充，让树有多层、页会被回收重用。
func roundValue(round int) []byte {
	return []byte(fmt.Sprintf("%06d-%s", round, strings.Repeat("x", 90)))
}

func roundOf(value []byte) (int, error) {
	return strconv.Atoi(string(value[:6]))
}

// readSnapshot 在读事务里扫描全表：必须恰好 wantKeys 条，且所有 value 属于同一轮。
// 返回这一轮的轮次。同一个事务里扫两遍，结果必须相同（快照在事务期间不变）。
func readSnapshot(t *tx, wantKeys int) (int, error) {
	scan := func() (int, error) {
		c := t.tree.newCursor()
		if err := c.first(); err != nil {
			return 0, err
		}
		count, round := 0, -1
		for c.valid() {
			r, err := roundOf(c.value())
			if err != nil {
				return 0, fmt.Errorf("key %s 的 value 损坏：%v", c.key(), err)
			}
			if round == -1 {
				round = r
			} else if r != round {
				return 0, fmt.Errorf("同一个快照里出现两轮的数据：%d 与 %d（key %s）", round, r, c.key())
			}
			count++
			if err := c.next(); err != nil {
				return 0, err
			}
		}
		if count != wantKeys {
			return 0, fmt.Errorf("快照里有 %d 条记录，期望 %d", count, wantKeys)
		}
		return round, nil
	}
	first, err := scan()
	if err != nil {
		return 0, err
	}
	second, err := scan()
	if err != nil {
		return 0, err
	}
	if first != second {
		return 0, fmt.Errorf("同一个读事务前后两次扫描看到不同的版本：%d 与 %d", first, second)
	}
	return first, nil
}

// 一个写者每轮把全部 key 改成新的一轮；多个读者不停地开读事务、扫描全表。
// 每个读者看到的必须是某一轮的完整版本，而且轮次不会倒退。
// 写者不断回收和重用页，所以这也检验"读者还在看的页不会被重用"在并发下成立。
func TestDBConcurrentReadersAndWriter(t *testing.T) {
	requireRace(t)
	const keys, rounds, readers = 120, 150, 4
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	d.sync = func() error { return nil }

	writeRound := func(round int) error {
		return d.update(func(tx *tx) error {
			for i := 0; i < keys; i++ {
				if err := tx.put([]byte(fmt.Sprintf("key-%04d", i)), roundValue(round)); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := writeRound(0); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	errs := make(chan error, readers+1)
	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lastRound := 0
			for {
				select {
				case <-done:
					return
				default:
				}
				var round int
				err := d.view(func(tx *tx) error {
					var err error
					round, err = readSnapshot(tx, keys)
					return err
				})
				if err != nil {
					errs <- err
					return
				}
				if round < lastRound {
					errs <- fmt.Errorf("读者先看到第 %d 轮，后来却看到第 %d 轮", lastRound, round)
					return
				}
				lastRound = round
			}
		}()
	}
	for round := 1; round <= rounds; round++ {
		if err := writeRound(round); err != nil {
			errs <- err
			break
		}
	}
	close(done)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	checkDB(t, d)
	err := d.view(func(tx *tx) error {
		round, err := readSnapshot(tx, keys)
		if err == nil && round != rounds {
			err = fmt.Errorf("最后看到第 %d 轮，期望 %d", round, rounds)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 读事务开始时的"复制 meta"与"登记版本号"必须是一个整体。
//
// 测试在两步之间（钩子）让另一个 goroutine 连续提交几个写事务：
//   - 两步在同一次持锁内：写事务开始时拿不到状态锁，要等读事务登记完才能继续，
//     钩子等到超时返回；之后写事务看得到这个读者，不会重用它的页。
//   - 两步之间没有锁，或分成两次持锁：写事务在钩子等待期间全部完成，它们看不到这个读者，
//     把它那个版本的页放出并重用；读事务随后读到被覆盖的页。
func TestDBReaderRegistrationIsAtomic(t *testing.T) {
	const keys = 120
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	d.sync = func() error { return nil }
	writeRound := func(round int) error {
		return d.update(func(tx *tx) error {
			for i := 0; i < keys; i++ {
				if err := tx.put([]byte(fmt.Sprintf("key-%04d", i)), roundValue(round)); err != nil {
					return err
				}
			}
			return nil
		})
	}
	for round := 0; round <= 2; round++ { // 读者将看到第 2 轮；待回收表里已有更早版本的页
		if err := writeRound(round); err != nil {
			t.Fatal(err)
		}
	}

	writersDone := make(chan error, 1)
	var hookUsed atomic.Bool
	d.hookAfterMetaCopy = func() {
		if !hookUsed.CompareAndSwap(false, true) {
			return
		}
		go func() {
			for round := 3; round <= 8; round++ {
				if err := writeRound(round); err != nil {
					writersDone <- err
					return
				}
			}
			writersDone <- nil
		}()
		select {
		case err := <-writersDone: // 写事务没有被挡住：把结果放回去，后面统一检查
			writersDone <- err
		case <-time.After(300 * time.Millisecond): // 写事务在等状态锁：正确的情况
		}
	}

	reader := mustBegin(t, d, false)
	defer reader.rollback()
	if err := <-writersDone; err != nil {
		t.Fatal(err)
	}
	round, err := readSnapshot(reader, keys)
	if err != nil {
		t.Fatalf("读事务开始时被插入了写事务，它的页被重用：%v", err)
	}
	if round != 2 {
		t.Fatalf("读事务看到第 %d 轮，期望第 2 轮", round)
	}
	if _, err := reader.tree.check(); err != nil {
		t.Fatalf("读事务的树损坏：%v", err)
	}
}

// 多个 goroutine 各自在写事务里"读出计数器、加一、写回"。写事务一个接一个执行，
// 所以不会丢失更新：最终的值等于总次数。
func TestDBConcurrentWritersSerialize(t *testing.T) {
	requireRace(t)
	const writers, perWriter = 4, 50
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	defer d.close()
	d.sync = func() error { return nil }
	dbPut(t, d, "counter", "0")

	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := d.update(func(tx *tx) error {
					value, _, err := tx.get([]byte("counter"))
					if err != nil {
						return err
					}
					n, err := strconv.Atoi(string(value))
					if err != nil {
						return err
					}
					return tx.put([]byte("counter"), []byte(strconv.Itoa(n+1)))
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if got, _ := readValue(t, d, "counter"); got != strconv.Itoa(writers*perWriter) {
		t.Fatalf("计数器 = %s，期望 %d：有更新丢失", got, writers*perWriter)
	}
	checkDB(t, d)
}

// close 等待进行中的事务结束；关闭之后不能再开始事务；重复关闭无效果。
func TestDBCloseWaitsForTransactions(t *testing.T) {
	d := openTestDB(t, filepath.Join(t.TempDir(), "kv.db"))
	dbPut(t, d, "a", "1")
	reader := mustBegin(t, d, false)

	closed := make(chan error, 1)
	go func() { closed <- d.close() }()
	select {
	case <-closed:
		t.Fatal("还有读事务没结束，close 就返回了")
	case <-time.After(100 * time.Millisecond):
	}
	// 读事务此时仍然可用：映射还没有解除。
	if v, found, err := reader.get([]byte("a")); err != nil || !found || string(v) != "1" {
		t.Fatalf("等待关闭期间读事务读到 %q, %v, %v", v, found, err)
	}
	reader.rollback()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("读事务结束后 close 仍未返回")
	}

	if _, err := d.begin(false); !errors.Is(err, ErrDBClosed) {
		t.Fatalf("关闭后开始读事务 err = %v", err)
	}
	if err := d.update(func(tx *tx) error { return nil }); !errors.Is(err, ErrDBClosed) {
		t.Fatalf("关闭后开始写事务 err = %v", err)
	}
	if err := d.close(); err != nil {
		t.Fatalf("重复关闭 err = %v", err)
	}
}
