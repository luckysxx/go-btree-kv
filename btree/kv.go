package btree

import "bytes"

// 本文件是包的对外接口：一个单文件、可恢复的有序 KV 存储。
//
//	db, err := btree.Open(path)
//	defer db.Close()
//	err = db.Put(key, value)
//	value, found, err := db.Get(key)
//	err = db.Update(func(tx *btree.Tx) error { ... })   // 多个操作，一起生效或一起不生效
//
// 并发：DB 可以被多个 goroutine 同时使用。同一时刻只有一个写事务（Put、Delete、Update 排队），
// 读（Get、Scan、View）数量不限，读与写互不等待。一个 Tx 只能在创建它的那次回调中、
// 由同一个 goroutine 使用。
//
// 可能返回的错误：ErrKeyTooLarge、ErrRecordTooLarge（输入被拒绝）；ErrInvalidDB、
// ErrVersionMismatch（打开时）；ErrDBClosed；ErrDBFailed（一次提交在写 meta 之后失败，
// 结果不确定，需要关闭后重开）；ErrTxFailed、ErrTxReadOnly、ErrTxClosed（事务使用不当或中途出错）；
// ErrCorruptTree、ErrPageNotFound（数据损坏）。

// DB 是一个打开的数据库文件。
type DB struct {
	d *db
}

// Tx 是一个事务，只在 Update 或 View 的回调期间有效。
type Tx struct {
	t *tx
}

// Open 打开或创建数据库文件。已有文件按 meta 恢复到最近一次提交成功的版本。
func Open(path string) (*DB, error) {
	d, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &DB{d: d}, nil
}

// Close 等待所有进行中的操作结束后关闭文件。之后的操作返回 ErrDBClosed。
func (db *DB) Close() error {
	return db.d.close()
}

// Update 在一个写事务里执行 fn：fn 返回 nil 则提交，全部修改一起生效；
// 返回错误则回滚，全部修改都不生效。提交本身失败时返回提交的错误（见 ErrDBFailed）。
func (db *DB) Update(fn func(tx *Tx) error) error {
	return db.d.update(func(t *tx) error { return fn(&Tx{t: t}) })
}

// View 在一个只读事务里执行 fn。fn 看到的是开始时最近一次提交的版本，
// 期间其他 goroutine 提交的修改对它不可见。
func (db *DB) View(fn func(tx *Tx) error) error {
	return db.d.view(func(t *tx) error { return fn(&Tx{t: t}) })
}

// Get 返回 key 的 value。返回的切片是副本，调用者可以任意使用和修改。
func (db *DB) Get(key []byte) (value []byte, found bool, err error) {
	err = db.View(func(tx *Tx) error {
		borrowed, ok, err := tx.Get(key)
		if err != nil {
			return err
		}
		value, found = bytes.Clone(borrowed), ok
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return value, found, nil
}

// Put 插入或覆盖一条记录，返回时已经持久。
func (db *DB) Put(key, value []byte) error {
	return db.Update(func(tx *Tx) error { return tx.Put(key, value) })
}

// Delete 删除 key，返回它是否存在；返回时已经持久。
func (db *DB) Delete(key []byte) (found bool, err error) {
	err = db.Update(func(tx *Tx) error {
		found, err = tx.Delete(key)
		return err
	})
	return found, err
}

// Scan 按 key 升序遍历 [start, end) 内的记录，对每条调用 fn；nil 表示该端不设界。
// fn 返回错误时停止并返回该错误。传给 fn 的 key 与 value 是借用的，只在这次调用期间有效。
func (db *DB) Scan(start, end []byte, fn func(key, value []byte) error) error {
	return db.View(func(tx *Tx) error { return tx.Scan(start, end, fn) })
}

// Get 返回 key 的 value。返回的切片借用页内字节，只在事务期间有效，不能修改；
// 需要保留时由调用者复制。
func (tx *Tx) Get(key []byte) (value []byte, found bool, err error) {
	return tx.t.get(key)
}

// Put 在事务内插入或覆盖一条记录。只读事务返回 ErrTxReadOnly。
func (tx *Tx) Put(key, value []byte) error {
	return tx.t.put(key, value)
}

// Delete 在事务内删除 key，返回它是否存在。只读事务返回 ErrTxReadOnly。
func (tx *Tx) Delete(key []byte) (found bool, err error) {
	return tx.t.delete(key)
}

// Scan 按 key 升序遍历 [start, end) 内的记录；nil 表示该端不设界。
// 传给 fn 的 key 与 value 是借用的，只在这次调用期间有效。
// fn 中不能修改本事务（Put、Delete）：修改会使正在进行的遍历失效。
func (tx *Tx) Scan(start, end []byte, fn func(key, value []byte) error) error {
	if tx.t.closed {
		return ErrTxClosed
	}
	if tx.t.failed != nil {
		return tx.t.failed
	}
	c := tx.t.tree.newCursor()
	var err error
	if start == nil {
		err = c.first()
	} else {
		err = c.seek(start)
	}
	if err != nil {
		return err
	}
	for c.valid() {
		if end != nil && bytes.Compare(c.key(), end) >= 0 {
			return nil
		}
		if err := fn(c.key(), c.value()); err != nil {
			return err
		}
		if err := c.next(); err != nil {
			return err
		}
	}
	return nil
}
