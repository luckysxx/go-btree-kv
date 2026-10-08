package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrCorruptTree 表示树结构损坏：页类型未知、内部页为空，或下降时回到访问过的页。
// 缺页不用它，直接沿用页存储的 ErrPageNotFound。
var ErrCorruptTree = errors.New("btree: corrupt tree")

// tree 是 B+Tree 的整树入口，只保存跨调用存活的状态：页存储与根页号。
//
// 页存储是 memPageStore 时，给 root 赋值就是发布；接到磁盘时页存储是事务 tx，
// root 只是事务内的根，真正的发布是 tx.commit 写 meta（见 tx.go）。
//
// 根始终是一张存在的页：空树的根是一张空叶子页（做法同 bbolt）。
// 因此 Get/Put/删空都不需要"没有根"的特例；root == 0 不是合法状态，
// 读取时会因页存储找不到页 0 而返回 ErrPageNotFound。
type tree struct {
	store pageStore
	root  uint64
}

// newTree 创建空树：保存一张空叶子页作为根。
func newTree(store pageStore) (*tree, error) {
	emptyLeaf := leafBuild(nil, 0) // type=叶子，nkeys=0，sibling=0
	rootID, err := store.save(emptyLeaf)
	if err != nil {
		return nil, err
	}
	return &tree{store: store, root: rootID}, nil
}

// get 从根查找 key。
//
//	找到        → (value, true, nil)
//	key 不存在  → (nil, false, nil)   空树也走这里：空叶子里查不到
//	树损坏/缺页 → (nil, false, err)
//
// 返回的 value 借用页内字节，调用者不能修改；有效期见 getFrom 中的说明。
func (t *tree) get(key []byte) ([]byte, bool, error) {
	return t.getFrom(t.root, key, nil)
}

// getFrom 从 pageID 这一页开始向下查找 key；visited 是本次查询从根到上一层经过的页号。
//
// 按页类型分三支：叶子页在页内查找；内部页选出孩子后递归；其他类型视为损坏。
// 页号重复出现（环）、内部页为空都返回 ErrCorruptTree；缺页原样返回 ErrPageNotFound。
// visited 是本次查询的局部路径，每层只走一个孩子，append 共用底层数组也不会互相干扰。
func (t *tree) getFrom(pageID uint64, key []byte, visited []uint64) ([]byte, bool, error) {
	for _, id := range visited {
		if pageID == id {
			return nil, false, fmt.Errorf("%w: page %d revisited", ErrCorruptTree, pageID)
		}
	}
	visited = append(visited, pageID)
	page, err := t.store.get(pageID)
	if err != nil {
		return nil, false, err
	}
	switch nodeType(page) {
	case NodeLeaf:
		// value 借用页内字节，不复制。只要这一页不被改写或回收，借用就有效：
		// 目前页存储既不覆盖已保存的页也不回收，GC 会让底层数组一直存活。
		// 引入页回收（D06）或接入 Pager 的 mmap（批次 4）后，需要重新约定借用的有效期。
		value, exists := leafGet(page, key)
		return value, exists, nil
	case NodeInternal:
		if nodeNKeys(page) == 0 {
			return nil, false, fmt.Errorf("%w: internal page %d has no keys", ErrCorruptTree, pageID)
		}
		childID := internalChild(page, key)
		return t.getFrom(childID, key, visited)
	default:
		return nil, false, fmt.Errorf("%w: page %d has unknown type %d", ErrCorruptTree, pageID, nodeType(page))
	}
}

// put 插入或覆盖 (key, val)。
//
// 路径复制：从根下降到叶子，叶子与路径上每一层都重建为新页并保存。
// 别人可能看到的页全部不变：在内存页存储上，拿着旧根仍能读到修改前的版本；
// 在事务中，已提交的页不变，事务自己新建的页可能被复用页号（见 pageStore.replace）。
// 根返回多项时（根分裂），用这些目录项新建一张内部页作为新根，树高 +1（见 commitRoot）。
// 只在全部成功后修改 t.root，发布点在 commitRoot。失败时 t.root 不变，树仍是旧版本；
// 已保存的新页从根不可达，成为垃圾（内存版本暂不回收）。
// key、val 在调用期间借用；结果页拥有自己的副本，调用返回后调用者可以复用输入。
func (t *tree) put(key, val []byte) error {
	// 能提前检查的先检查：此时还没有保存任何页。
	// value 过大由叶子层的 leafInsertSplit 报 ErrRecordTooLarge，同样发生在保存之前。
	if len(key) > maxKeySize {
		return fmt.Errorf("%w: %d bytes", ErrKeyTooLarge, len(key))
	}
	entries, err := t.insert(t.root, key, val, nil)
	if err != nil {
		return err
	}
	return t.commitRoot(entries)
}

// commitRoot 把根这一层交回的目录项变成新根并发布（在事务中只更新事务内的根），是 put 与 delete 共用的唯一发布点：
//
//	0 项 → 树已删空，新建空叶子作根（空树 = 空叶子根）
//	多项 → 根分裂，新建一张内部页作根，树高 +1
//	1 项 → 就是新根；若它是只有一个孩子的内部页，逐层提升孩子（根收缩），树高 -1
//
// 只在新根页全部保存成功后修改 t.root；失败时 t.root 不变。
func (t *tree) commitRoot(entries []internalEntry) error {
	var newRoot uint64
	switch {
	case len(entries) == 0:
		emptyLeaf := leafBuild(nil, 0)
		rootID, err := t.store.save(emptyLeaf)
		if err != nil {
			return err
		}
		newRoot = rootID
	case len(entries) > 1:
		// 根最多返回 3 项：叶子最多分 3 页，内部页最多分 2 页。
		// 每项 ≤ 2+4+maxKeySize+8 = 1014 字节，3 项加页头 = 3054 ≤ 4096，一页一定装得下。
		// 该结论依赖 maxKeySize ≤ 1000；若放宽上限，需要改成循环。
		newRootPage, err := internalBuild(entries)
		if err != nil {
			return err
		}
		rootID, err := t.store.save(newRootPage)
		if err != nil {
			return err
		}
		newRoot = rootID
	default:
		rootID, err := t.shrinkRoot(entries[0].childPageID)
		if err != nil {
			return err
		}
		newRoot = rootID
	}
	t.root = newRoot // 发布新版本：唯一修改 t.root 的地方，必须在所有页都保存之后
	return nil
}

// shrinkRoot 从 rootID 开始，只要根是只有一个孩子的内部页，就改用这个孩子作根。
// 这样的内部页不起路由作用，只让每次操作多走一层。返回最终的根页号；不新建页，
// 被跳过的页交给页存储 free。
func (t *tree) shrinkRoot(rootID uint64) (uint64, error) {
	var visited []uint64
	for {
		for _, id := range visited {
			if id == rootID {
				return 0, fmt.Errorf("%w: page %d revisited", ErrCorruptTree, rootID)
			}
		}
		visited = append(visited, rootID)
		page, err := t.store.get(rootID)
		if err != nil {
			return 0, err
		}
		if nodeType(page) != NodeInternal || nodeNKeys(page) != 1 {
			return rootID, nil
		}
		childID := internalChildAt(page, 0)
		if err := t.store.free(rootID); err != nil { // 被跳过的这一层不再属于树
			return 0, err
		}
		rootID = childID // 唯一的孩子直接当根
	}
}

// delete 删除 key，返回 key 是否存在。
//
// key 不存在时树完全不变：不新建页，也不发布新根。存在时与 put 一样路径复制，
// 旧根仍是删除前的完整版本；叶子删空时从父页摘掉它，父页因此变空时继续向上摘，
// 最后由 commitRoot 处理根收缩与删空。不做合并与借位，删除后叶子可能很稀疏。
func (t *tree) delete(key []byte) (bool, error) {
	if len(key) > maxKeySize {
		return false, nil // 超过上限的 key 不可能被写入过
	}
	entries, changed, err := t.remove(t.root, key, nil)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := t.commitRoot(entries); err != nil {
		return false, err
	}
	return true, nil
}

// remove 在以 pageID 为根的子树中删除 key，结果有三种：
//
//	changed == false                 → key 不存在，子树不变，父页也不必改
//	changed == true，len(entries) > 0 → 用 entries 替换这个孩子（通常 1 项；新 minKey 可能更长，父页可能分裂）
//	changed == true，len(entries) == 0 → 这棵子树已空，父页要删掉它的目录项
//
// visited 是本次操作从根到上一层经过的页号，用于发现环（同 insert）。
func (t *tree) remove(pageID uint64, key []byte, visited []uint64) ([]internalEntry, bool, error) {
	for _, id := range visited {
		if pageID == id {
			return nil, false, fmt.Errorf("%w: page %d revisited", ErrCorruptTree, pageID)
		}
	}
	visited = append(visited, pageID)
	page, err := t.store.get(pageID)
	if err != nil {
		return nil, false, err
	}
	switch nodeType(page) {
	case NodeLeaf:
		idx, exists := leafSeek(page, key)
		if !exists {
			return nil, false, nil
		}
		newPage := leafDelete(page, idx)
		if nodeNKeys(newPage) == 0 {
			// 叶子删空：交给父页摘掉它。若它就是根，commitRoot 会换上新的空叶子根。
			if err := t.store.free(pageID); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
		entries, err := t.savePages(pageID, [][]byte{newPage})
		return entries, true, err
	case NodeInternal:
		if nodeNKeys(page) == 0 {
			return nil, false, fmt.Errorf("%w: internal page %d has no keys", ErrCorruptTree, pageID)
		}
		childIndex := internalChildIndex(page, key)
		childID := internalChildAt(page, childIndex)
		childEntries, changed, err := t.remove(childID, key, visited)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return nil, false, nil // 孩子没变：本页也不复制
		}
		if len(childEntries) == 0 {
			// 孩子整棵没了。它若是本页唯一的孩子，本页也空了，继续让上层摘掉本页。
			if nodeNKeys(page) == 1 {
				if err := t.store.free(pageID); err != nil {
					return nil, false, err
				}
				return nil, true, nil
			}
			newPage, err := internalRemoveChild(page, childIndex)
			if err != nil {
				return nil, false, err
			}
			entries, err := t.savePages(pageID, [][]byte{newPage})
			return entries, true, err
		}
		// 孩子被替换：同 insert。新 minKey 可能比旧的长，父页可能分裂，所以用 Split 版本。
		newPages, err := internalReplaceChildSplit(page, childIndex, childEntries)
		if err != nil {
			return nil, false, err
		}
		entries, err := t.savePages(pageID, newPages)
		return entries, true, err
	default:
		return nil, false, fmt.Errorf("%w: page %d has unknown type %d", ErrCorruptTree, pageID, nodeType(page))
	}
}

// insert 在以 pageID 为根的子树中插入或覆盖 (key, val)，返回替换这棵子树所需的目录项：
// 每张新页一项 {页内第一个 key, 新页号}；没有分裂时只有一项，分裂时 2～3 项。
// visited 是本次操作从根到上一层经过的页号，用于发现环（同 getFrom）。
func (t *tree) insert(pageID uint64, key, val []byte, visited []uint64) ([]internalEntry, error) {
	for _, id := range visited {
		if pageID == id {
			return nil, fmt.Errorf("%w: page %d revisited", ErrCorruptTree, pageID)
		}
	}
	visited = append(visited, pageID)
	page, err := t.store.get(pageID)
	if err != nil {
		return nil, err
	}
	switch nodeType(page) {
	case NodeLeaf:
		newPages, err := leafInsertSplit(page, key, val)
		if err != nil {
			return nil, err
		}
		return t.savePages(pageID, newPages)
	case NodeInternal:
		if nodeNKeys(page) == 0 {
			return nil, fmt.Errorf("%w: internal page %d has no keys", ErrCorruptTree, pageID)
		}
		// 下标用来改父页（替换第几项），页号用来取孩子页；两者都要留到孩子返回之后。
		childIndex := internalChildIndex(page, key)
		childID := binary.LittleEndian.Uint64(getValue(page, childIndex))
		childEntries, err := t.insert(childID, key, val, visited)
		if err != nil {
			return nil, err
		}
		newPages, err := internalReplaceChildSplit(page, childIndex, childEntries)
		if err != nil {
			return nil, err
		}
		return t.savePages(pageID, newPages) // 父页重建；是否分配新页号由页存储决定（见 pageStore.replace）
	default:
		return nil, fmt.Errorf("%w: page %d has unknown type %d", ErrCorruptTree, pageID, nodeType(page))
	}
}

// savePages 保存替换 oldID 这一页的新页，返回每页的目录项。pages 至少一页，每页至少有一条记录。
//
// 第一页是 oldID 的新版本，交给 replace：页存储可以复用 oldID（事务自己新建的页），
// 也可以分配新页号（已提交的页，必须路径复制）。分裂多出来的页用 save 分配新页号。
// minKey 取页内第一个 key：新页的第一个 key 就是这棵子树的最小 key，
// 所以插入更小的 key 后，父目录中的 minKey 会随之更新，不需要单独处理。
func (t *tree) savePages(oldID uint64, pages [][]byte) ([]internalEntry, error) {
	entries := make([]internalEntry, 0, len(pages))
	for i, page := range pages {
		var pageID uint64
		var err error
		if i == 0 {
			pageID, err = t.store.replace(oldID, page)
		} else {
			pageID, err = t.store.save(page)
		}
		if err != nil {
			return nil, err
		}
		minKey := getKey(page, 0) // 借用 page 的字节；父页构建时会复制
		entries = append(entries, internalEntry{minKey: minKey, childPageID: pageID})
	}
	return entries, nil
}
