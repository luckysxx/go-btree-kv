package btree

import (
	"bytes"
	"fmt"
)

// check 遍历整棵树，验证结构不变量，返回树高（只有根叶子时为 1）。做法类似 bbolt 的 tx.Check：
// 正常读写路径信任页内容，需要时单独做一次全树检查。检查项：
//
//   - 每张页可读、类型已知；每张页只被引用一次（同时排除环和共享子树）
//   - 所有叶子深度相同；内部页非空；除根以外的叶子非空；sibling 为 0（CoW 树不维护叶子链）
//   - 页内 key 严格递增；目录 minKey 等于孩子子树的实际最小 key
//   - 每个孩子子树的所有 key 落在 [本项 minKey, 下一项 minKey) 内，即子树之间范围不重叠
//
// 发现问题返回 ErrCorruptTree（缺页返回 ErrPageNotFound），错误中带页号与原因。
// 只检查内存中的逻辑结构，不涉及落盘、提交或恢复。
func (t *tree) check() (int, error) {
	checker := treeChecker{tree: t, seen: make(map[uint64]bool)}
	if _, err := checker.walk(t.root, 1, nil, nil); err != nil {
		return 0, err
	}
	return checker.leafDepth, nil
}

type treeChecker struct {
	tree      *tree
	seen      map[uint64]bool // 已访问过的页，第二次遇到说明被引用了两次
	leafDepth int             // 第一张叶子的深度，其余叶子必须相同
}

// walk 检查以 pageID 为根的子树，要求其所有 key 落在 [low, high) 内（nil 表示不限），
// 返回子树最小 key（空叶子根返回 nil）。
func (c *treeChecker) walk(pageID uint64, depth int, low, high []byte) ([]byte, error) {
	if c.seen[pageID] {
		return nil, corrupt(pageID, "referenced more than once")
	}
	c.seen[pageID] = true
	page, err := c.tree.store.get(pageID)
	if err != nil {
		return nil, err
	}
	count := int(nodeNKeys(page))
	if nodeSibling(page) != 0 {
		return nil, corrupt(pageID, "sibling is not 0")
	}
	// 页内 key 严格递增，且都在 [low, high) 内。
	for i := 0; i < count; i++ {
		key := getKey(page, i)
		if i > 0 && bytes.Compare(getKey(page, i-1), key) >= 0 {
			return nil, corrupt(pageID, fmt.Sprintf("key %d not strictly increasing", i))
		}
		if (low != nil && bytes.Compare(key, low) < 0) || (high != nil && bytes.Compare(key, high) >= 0) {
			return nil, corrupt(pageID, fmt.Sprintf("key %q outside parent range", key))
		}
	}

	switch nodeType(page) {
	case NodeLeaf:
		if c.leafDepth == 0 {
			c.leafDepth = depth
		} else if depth != c.leafDepth {
			return nil, corrupt(pageID, fmt.Sprintf("leaf depth %d, other leaves %d", depth, c.leafDepth))
		}
		if count == 0 {
			if pageID != c.tree.root {
				return nil, corrupt(pageID, "non-root leaf is empty")
			}
			return nil, nil
		}
		return getKey(page, 0), nil
	case NodeInternal:
		if count == 0 {
			return nil, corrupt(pageID, "internal page has no keys")
		}
		for i := 0; i < count; i++ {
			minKey := getKey(page, i)
			childHigh := high
			if i+1 < count {
				childHigh = getKey(page, i+1) // 孩子的 key 必须小于下一项的 minKey
			}
			childMin, err := c.walk(internalChildAt(page, i), depth+1, minKey, childHigh)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(childMin, minKey) {
				return nil, corrupt(pageID, fmt.Sprintf("entry %d minKey %q, child subtree min %q", i, minKey, childMin))
			}
		}
		return getKey(page, 0), nil
	default:
		return nil, corrupt(pageID, fmt.Sprintf("unknown type %d", nodeType(page)))
	}
}

func corrupt(pageID uint64, reason string) error {
	return fmt.Errorf("%w: page %d %s", ErrCorruptTree, pageID, reason)
}
