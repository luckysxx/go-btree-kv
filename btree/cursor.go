package btree

import (
	"fmt"
)

// cursorFrame 是游标路径上的一层：哪张页，以及在这张页里指向第几项。
// 内部页的 index 是当前所在孩子的下标；叶子页的 index 是当前记录的下标。
type cursorFrame struct {
	pageID uint64
	page   []byte
	index  int
}

// exhausted 报告这一层的项是否已用完：叶子是记录读完了，内部页是孩子走完了。
func (f cursorFrame) exhausted() bool {
	return f.index >= int(nodeNKeys(f.page))
}

// cursor 在某个根的快照上按 key 升序遍历记录。
//
// 不使用叶子 sibling：CoW 下维护叶子链需要复制左侧所有叶子，因此改为保存从根到叶子的路径（栈），
// 读完一张叶子后沿栈回到上层、换到下一个孩子，再向下走到最左叶子。做法同 bbolt 的 Cursor。
//
// 游标创建时拿住当时的根。在内存页存储上，之后树再 put 并发布新根，游标仍在旧版本上遍历，
// 看到一致快照；读事务中的游标同理。写事务例外：事务自己新建的页会被复用页号，
// put/delete 之后本事务此前创建的游标失效，需要重新定位（见 tx 的说明）。
// key()/value() 借用页内字节，有效期同 get（页不被回收即有效）。非并发安全。
// 范围扫描约定左闭右开 [start, end)：seek(start) 定位，调用者读到 key >= end 时自行停止。
type cursor struct {
	store pageStore
	root  uint64
	stack []cursorFrame // stack[0] 是根，最后一项是当前叶子；为空表示尚未定位或已遍历完
}

// newCursor 在树当前的根上创建游标。创建后需先调用 first 或 seek 定位。
func (t *tree) newCursor() *cursor {
	return &cursor{store: t.store, root: t.root}
}

// valid 报告游标是否停在一条用户记录上。用户记录只存在叶子里，所以要求栈顶是叶子且未读完；
// 栈顶是内部页只会出现在 nextLeaf 执行途中，此时 valid 为 false。
func (c *cursor) valid() bool {
	if len(c.stack) == 0 {
		return false
	}
	top := c.stack[len(c.stack)-1]
	return nodeType(top.page) == NodeLeaf && !top.exhausted()
}

// key 返回当前记录的 key；要求 valid() 为 true。
func (c *cursor) key() []byte {
	leaf := c.stack[len(c.stack)-1]
	return getKey(leaf.page, leaf.index)
}

// value 返回当前记录的 value；要求 valid() 为 true。
func (c *cursor) value() []byte {
	leaf := c.stack[len(c.stack)-1]
	return getValue(leaf.page, leaf.index)
}

// first 定位到最小的记录；空树时游标无效。出错时游标无效并返回错误。
func (c *cursor) first() error {
	c.stack = c.stack[:0]
	if err := c.descendLeftmost(c.root); err != nil {
		c.stack = nil
		return err
	}
	return c.skipExhaustedLeaf()
}

// seek 定位到第一条 key >= target 的记录；不存在时游标无效。出错时游标无效并返回错误。
func (c *cursor) seek(target []byte) error {
	c.stack = c.stack[:0]
	pageID := c.root
	for {
		page, err := c.push(pageID)
		if err != nil {
			c.stack = nil
			return err
		}
		top := &c.stack[len(c.stack)-1]
		if nodeType(page) == NodeLeaf {
			// 可能等于 nkeys：target 比这张叶子的所有 key 都大，要走到下一张叶子。
			top.index, _ = leafSeek(page, target)
			return c.skipExhaustedLeaf()
		}
		top.index = internalChildIndex(page, target)
		pageID = internalChildAt(page, top.index)
	}
}

// next 前进到下一条记录；后面没有记录时游标无效。要求调用前 valid() 为 true。
func (c *cursor) next() error {
	leaf := &c.stack[len(c.stack)-1]
	leaf.index++
	return c.skipExhaustedLeaf()
}

// skipExhaustedLeaf 在当前叶子已读完（index == nkeys）时移到下一张叶子；否则什么都不做。
func (c *cursor) skipExhaustedLeaf() error {
	if len(c.stack) == 0 || c.valid() {
		return nil
	}
	if err := c.nextLeaf(); err != nil {
		c.stack = nil // 出错后游标无效，调用者不会读到半途的状态
		return err
	}
	return nil
}

// nextLeaf 在栈顶叶子已读完时，把游标移到下一张非空叶子的第一条记录；
// 整棵树都走完时清空栈并返回 nil（valid() 为 false）。
func (c *cursor) nextLeaf() error {
	// 每轮先弹出栈顶：第一轮弹出读完的叶子；某层孩子走完时弹出该层；
	// 走到空叶子（D06 删除后可能出现）时，下一轮也会把它弹掉继续找。
	for {
		c.stack = c.stack[:len(c.stack)-1]
		if len(c.stack) == 0 {
			return nil
		}
		top := &c.stack[len(c.stack)-1] // 内部页；descendLeftmost 会 append，之后不再使用 top
		top.index++                     // 换到下一个孩子
		if top.exhausted() {
			continue // 这一层的孩子也走完了，下一轮弹出它，继续往上
		}
		childID := internalChildAt(top.page, top.index)
		err := c.descendLeftmost(childID)
		if err != nil {
			return err
		}
		if c.valid() {
			return nil // 停在非空叶子的第一条记录上
		}
	}
}

// descendLeftmost 从 pageID 开始一路走第 0 个孩子直到叶子，把经过的每层压栈（index 均为 0）。
func (c *cursor) descendLeftmost(pageID uint64) error {
	for {
		page, err := c.push(pageID)
		if err != nil {
			return err
		}
		if nodeType(page) == NodeLeaf {
			return nil
		}
		pageID = internalChildAt(page, 0)
	}
}

// push 读取 pageID 并压栈（index 为 0）。与栈中已有页重复视为环；同时检查页类型与空内部页。
func (c *cursor) push(pageID uint64) ([]byte, error) {
	for _, frame := range c.stack {
		if frame.pageID == pageID {
			return nil, fmt.Errorf("%w: page %d revisited", ErrCorruptTree, pageID)
		}
	}
	page, err := c.store.get(pageID)
	if err != nil {
		return nil, err
	}
	switch nodeType(page) {
	case NodeLeaf:
	case NodeInternal:
		if nodeNKeys(page) == 0 {
			return nil, fmt.Errorf("%w: internal page %d has no keys", ErrCorruptTree, pageID)
		}
	default:
		return nil, fmt.Errorf("%w: page %d has unknown type %d", ErrCorruptTree, pageID, nodeType(page))
	}
	c.stack = append(c.stack, cursorFrame{pageID: pageID, page: page})
	return page, nil
}
