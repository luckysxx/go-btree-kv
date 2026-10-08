package btree_test

// 只使用对外接口的测试：确认导出的那一层足以完成增删改查、范围扫描、事务与重开。

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"dbstudy/btree"
)

func openKV(t *testing.T, path string) *btree.DB {
	t.Helper()
	db, err := btree.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestKVPutGetDeleteAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	db := openKV(t, path)
	if _, found, err := db.Get([]byte("a")); err != nil || found {
		t.Fatalf("空库 Get = %v, %v", found, err)
	}
	if err := db.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	value, found, err := db.Get([]byte("a"))
	if err != nil || !found || string(value) != "1" {
		t.Fatalf("Get a = %q, %v, %v", value, found, err)
	}
	value[0] = 'X' // Get 返回副本：改它不影响数据库
	if again, _, _ := db.Get([]byte("a")); string(again) != "1" {
		t.Fatalf("修改返回的 value 后再读到 %q", again)
	}
	if found, err := db.Delete([]byte("b")); err != nil || !found {
		t.Fatalf("Delete b = %v, %v", found, err)
	}
	if found, err := db.Delete([]byte("b")); err != nil || found {
		t.Fatalf("再次 Delete b = %v, %v", found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Get([]byte("a")); !errors.Is(err, btree.ErrDBClosed) {
		t.Fatalf("关闭后 Get err = %v", err)
	}

	db = openKV(t, path)
	defer db.Close()
	if value, found, _ := db.Get([]byte("a")); !found || string(value) != "1" {
		t.Fatalf("重开后 a = %q, %v", value, found)
	}
	if _, found, _ := db.Get([]byte("b")); found {
		t.Fatal("重开后已删除的 b 又出现了")
	}
}

func TestKVScan(t *testing.T) {
	db := openKV(t, filepath.Join(t.TempDir(), "kv.db"))
	defer db.Close()
	err := db.Update(func(tx *btree.Tx) error {
		for i := 0; i < 500; i++ {
			if err := tx.Put([]byte(fmt.Sprintf("key-%03d", i)), []byte(fmt.Sprint(i))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	collect := func(start, end []byte) []string {
		var keys []string
		if err := db.Scan(start, end, func(key, value []byte) error {
			keys = append(keys, string(key))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return keys
	}
	if got := collect(nil, nil); len(got) != 500 || got[0] != "key-000" || got[499] != "key-499" {
		t.Fatalf("全表扫描得到 %d 条", len(got))
	}
	// 左闭右开：包含 key-100，不包含 key-103。
	if got := collect([]byte("key-100"), []byte("key-103")); fmt.Sprint(got) != "[key-100 key-101 key-102]" {
		t.Fatalf("区间扫描 = %v", got)
	}
	if got := collect([]byte("key-498"), nil); fmt.Sprint(got) != "[key-498 key-499]" {
		t.Fatalf("只有下界 = %v", got)
	}
	if got := collect(nil, []byte("key-002")); fmt.Sprint(got) != "[key-000 key-001]" {
		t.Fatalf("只有上界 = %v", got)
	}
	if got := collect([]byte("zzz"), nil); len(got) != 0 {
		t.Fatalf("越过末尾 = %v", got)
	}
	// 回调返回错误：立即停止并原样返回。
	errStop := errors.New("stop")
	seen := 0
	err = db.Scan(nil, nil, func(key, value []byte) error {
		seen++
		if seen == 3 {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) || seen != 3 {
		t.Fatalf("提前停止：err = %v，看到 %d 条", err, seen)
	}
}

func TestKVUpdateIsAtomic(t *testing.T) {
	db := openKV(t, filepath.Join(t.TempDir(), "kv.db"))
	defer db.Close()
	if err := db.Put([]byte("alice"), []byte("100")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("bob"), []byte("100")); err != nil {
		t.Fatal(err)
	}
	// 回滚：两处修改都不生效。
	errAbort := errors.New("abort")
	err := db.Update(func(tx *btree.Tx) error {
		if err := tx.Put([]byte("alice"), []byte("90")); err != nil {
			return err
		}
		if err := tx.Put([]byte("bob"), []byte("110")); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	if v, _, _ := db.Get([]byte("alice")); string(v) != "100" {
		t.Fatalf("回滚后 alice = %s", v)
	}
	// 提交：事务内读得到自己的修改；提交后两处一起生效。
	err = db.Update(func(tx *btree.Tx) error {
		if err := tx.Put([]byte("alice"), []byte("90")); err != nil {
			return err
		}
		if v, _, _ := tx.Get([]byte("alice")); string(v) != "90" {
			return fmt.Errorf("事务内读到 alice = %s", v)
		}
		return tx.Put([]byte("bob"), []byte("110"))
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := db.Get([]byte("alice"))
	b, _, _ := db.Get([]byte("bob"))
	if string(a) != "90" || string(b) != "110" {
		t.Fatalf("提交后 alice = %s, bob = %s", a, b)
	}
	// 只读事务不能写。
	err = db.View(func(tx *btree.Tx) error { return tx.Put([]byte("x"), []byte("y")) })
	if !errors.Is(err, btree.ErrTxReadOnly) {
		t.Fatalf("View 中 Put err = %v", err)
	}
	// 输入被拒绝。
	if err := db.Put(bytes.Repeat([]byte("k"), 2000), []byte("v")); !errors.Is(err, btree.ErrKeyTooLarge) {
		t.Fatalf("过长的 key err = %v", err)
	}
}

// 多个 goroutine 同时调用对外接口。只在竞争检测下运行（go test -race）。
func TestKVConcurrentUse(t *testing.T) {
	if !btree.RaceEnabled {
		t.Skip("并发测试需要竞争检测：go test -race")
	}
	db := openKV(t, filepath.Join(t.TempDir(), "kv.db"))
	defer db.Close()
	const writers, readers, perWriter = 2, 4, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers+readers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := db.Put([]byte(fmt.Sprintf("w%d-%03d", w, i)), []byte("v")); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				previous := ""
				err := db.Scan(nil, nil, func(key, value []byte) error {
					if string(key) <= previous {
						return fmt.Errorf("扫描顺序错误：%s 在 %s 之后", key, previous)
					}
					previous = string(key)
					return nil
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
	count := 0
	if err := db.Scan(nil, nil, func(key, value []byte) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != writers*perWriter {
		t.Fatalf("共 %d 条，期望 %d", count, writers*perWriter)
	}
}
