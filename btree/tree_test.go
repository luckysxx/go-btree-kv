package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// mustNewTree 创建空树；测试随后可把 root 换成手工搭建的根。
func mustNewTree(t *testing.T, store *memPageStore) *tree {
	t.Helper()
	tr, err := newTree(store)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// saveLeafPage 编码一张叶子页并存入 store，返回页号。
func saveLeafPage(t *testing.T, store *memPageStore, pairs [][2]string) uint64 {
	t.Helper()
	id, err := store.save(buildLeaf(pairs))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// saveInternalPage 编码一张内部页并存入 store，返回页号。
func saveInternalPage(t *testing.T, store *memPageStore, entries []internalEntry) uint64 {
	t.Helper()
	page, err := internalBuild(entries)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.save(page)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type getCase struct {
	key       string
	wantValue string
	wantFound bool
}

func checkGets(t *testing.T, tr *tree, cases []getCase) {
	t.Helper()
	for _, c := range cases {
		value, found, err := tr.get([]byte(c.key))
		if err != nil {
			t.Fatalf("get(%q) 意外出错：%v", c.key, err)
		}
		if found != c.wantFound || string(value) != c.wantValue {
			t.Fatalf("get(%q) = %q, %v，期望 %q, %v", c.key, value, found, c.wantValue, c.wantFound)
		}
	}
}

func TestTreeGetEmpty(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	// 空树的根是一张真实存在的空叶子页。
	rootPage, err := store.get(tr.root)
	if err != nil {
		t.Fatalf("空树的根页不存在：%v", err)
	}
	if tr.root == 0 || nodeType(rootPage) != NodeLeaf || nodeNKeys(rootPage) != 0 {
		t.Fatalf("空树根不是空叶子：root=%d type=%d nkeys=%d", tr.root, nodeType(rootPage), nodeNKeys(rootPage))
	}
	checkGets(t, tr, []getCase{
		{"", "", false},
		{"ant", "", false},
	})
}

func TestTreeGetSingleLeaf(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	tr.root = saveLeafPage(t, store, [][2]string{{"ant", "a"}, {"cat", "c"}})
	checkGets(t, tr, []getCase{
		{"ant", "a", true},
		{"cat", "c", true},
		{"aaa", "", false}, // 比所有 key 小
		{"bee", "", false}, // 夹在中间
		{"zoo", "", false}, // 比所有 key 大
	})
}

// 两层：根目录 → 三张叶子。
func TestTreeGetTwoLevels(t *testing.T) {
	store := newMemPageStore()
	leafA := saveLeafPage(t, store, [][2]string{{"ant", "a"}, {"cat", "c"}})
	leafD := saveLeafPage(t, store, [][2]string{{"dog", "d"}, {"fox", "f"}})
	leafP := saveLeafPage(t, store, [][2]string{{"pig", "p"}, {"zoo", "z"}})
	tr := mustNewTree(t, store)
	tr.root = saveInternalPage(t, store, []internalEntry{
		{[]byte("ant"), leafA}, {[]byte("dog"), leafD}, {[]byte("pig"), leafP},
	})
	checkGets(t, tr, []getCase{
		{"ant", "a", true}, // 首 key，也是目录 key
		{"cat", "c", true},
		{"dog", "d", true}, // 恰好等于分隔 key
		{"fox", "f", true},
		{"pig", "p", true},
		{"zoo", "z", true}, // 尾 key
		{"aaa", "", false}, // 小于首 key：走第一个孩子后未命中
		{"cow", "", false}, // 两张叶子之间的空隙
		{"emu", "", false}, // 叶子内部的空隙
		{"zzz", "", false}, // 大于所有 key
	})
}

// 三层：根 → 两个内部页 → 四张叶子。
func TestTreeGetThreeLevels(t *testing.T) {
	store := newMemPageStore()
	leaf1 := saveLeafPage(t, store, [][2]string{{"a1", "1"}, {"a2", "2"}})
	leaf2 := saveLeafPage(t, store, [][2]string{{"b1", "3"}, {"b2", "4"}})
	leaf3 := saveLeafPage(t, store, [][2]string{{"c1", "5"}, {"c2", "6"}})
	leaf4 := saveLeafPage(t, store, [][2]string{{"d1", "7"}, {"d2", "8"}})
	left := saveInternalPage(t, store, []internalEntry{{[]byte("a1"), leaf1}, {[]byte("b1"), leaf2}})
	right := saveInternalPage(t, store, []internalEntry{{[]byte("c1"), leaf3}, {[]byte("d1"), leaf4}})
	tr := mustNewTree(t, store)
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a1"), left}, {[]byte("c1"), right}})
	checkGets(t, tr, []getCase{
		{"a1", "1", true},
		{"b2", "4", true},
		{"c1", "5", true}, // 根与中间层的分隔 key
		{"d2", "8", true},
		{"b3", "", false}, // 左子树最右空隙
		{"e0", "", false},
	})
}

func TestTreeGetCorruption(t *testing.T) {
	t.Run("root 为 0", func(t *testing.T) {
		tr := mustNewTree(t, newMemPageStore())
		tr.root = 0 // 根始终存在，0 不再表示空树
		if _, _, err := tr.get([]byte("ant")); !errors.Is(err, ErrPageNotFound) {
			t.Fatalf("err = %v，期望 ErrPageNotFound", err)
		}
	})

	t.Run("根页号不存在", func(t *testing.T) {
		tr := mustNewTree(t, newMemPageStore())
		tr.root = 42
		_, found, err := tr.get([]byte("ant"))
		if !errors.Is(err, ErrPageNotFound) || found {
			t.Fatalf("err = %v, found = %v，期望 ErrPageNotFound", err, found)
		}
	})

	t.Run("孩子页号不存在", func(t *testing.T) {
		store := newMemPageStore()
		tr := mustNewTree(t, store)
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), 99}})
		_, _, err := tr.get([]byte("ant"))
		if !errors.Is(err, ErrPageNotFound) {
			t.Fatalf("err = %v，期望 ErrPageNotFound", err)
		}
	})

	t.Run("未知页类型", func(t *testing.T) {
		store := newMemPageStore()
		page := make([]byte, PageSize)
		binary.LittleEndian.PutUint16(page[offType:], 7)
		id, err := store.save(page)
		if err != nil {
			t.Fatal(err)
		}
		tr := mustNewTree(t, store)
		tr.root = id
		if _, _, err := tr.get([]byte("ant")); !errors.Is(err, ErrCorruptTree) {
			t.Fatalf("err = %v，期望 ErrCorruptTree", err)
		}
	})

	t.Run("空内部页", func(t *testing.T) {
		store := newMemPageStore()
		page := make([]byte, PageSize)
		binary.LittleEndian.PutUint16(page[offType:], NodeInternal) // nkeys 保持 0
		id, err := store.save(page)
		if err != nil {
			t.Fatal(err)
		}
		tr := mustNewTree(t, store)
		tr.root = id
		if _, _, err := tr.get([]byte("ant")); !errors.Is(err, ErrCorruptTree) {
			t.Fatalf("err = %v，期望 ErrCorruptTree", err)
		}
	})

	t.Run("孩子指向自己", func(t *testing.T) {
		store := newMemPageStore()
		tr := mustNewTree(t, store) // 空叶子根占用一个页号，所以先建树再取 nextID
		selfID := store.nextID      // 下一次 save 将分配的页号
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), selfID}})
		if tr.root != selfID {
			t.Fatalf("测试前提不成立：root = %d, selfID = %d", tr.root, selfID)
		}
		if _, _, err := tr.get([]byte("ant")); !errors.Is(err, ErrCorruptTree) {
			t.Fatalf("err = %v，期望 ErrCorruptTree", err)
		}
	})

	t.Run("孩子指回祖先", func(t *testing.T) {
		store := newMemPageStore()
		tr := mustNewTree(t, store)
		rootID := store.nextID
		childID := rootID + 1
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), childID}})
		saveInternalPage(t, store, []internalEntry{{[]byte("ant"), rootID}})
		if _, _, err := tr.get([]byte("ant")); !errors.Is(err, ErrCorruptTree) {
			t.Fatalf("err = %v，期望 ErrCorruptTree", err)
		}
	})
}

// buildTwoLevelTree 手工搭建两层树：root [ant→L1{ant,cat}, dog→L2{dog,fox}]。
func buildTwoLevelTree(t *testing.T) (*memPageStore, *tree) {
	t.Helper()
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	leaf1 := saveLeafPage(t, store, [][2]string{{"ant", "a"}, {"cat", "c"}})
	leaf2 := saveLeafPage(t, store, [][2]string{{"dog", "d"}, {"fox", "f"}})
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), leaf1}, {[]byte("dog"), leaf2}})
	return store, tr
}

func mustPut(t *testing.T, tr *tree, key, val string) {
	t.Helper()
	if err := tr.put([]byte(key), []byte(val)); err != nil {
		t.Fatalf("put(%q) 出错：%v", key, err)
	}
}

// checkModel 用参考 map 对照整棵树：map 里的每个 key 都能读到相同 value，absent 中的 key 都读不到。
func checkModel(t *testing.T, tr *tree, model map[string]string, absent []string) {
	t.Helper()
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	sort.Strings(keys) // 固定顺序，失败信息可复现
	for _, key := range keys {
		value, found, err := tr.get([]byte(key))
		if err != nil || !found || string(value) != model[key] {
			t.Fatalf("get(%q) = %q, %v, %v，期望 %q", key, value, found, err, model[key])
		}
	}
	for _, key := range absent {
		if _, found, err := tr.get([]byte(key)); err != nil || found {
			t.Fatalf("get(%q) = found %v, err %v，期望不存在", key, found, err)
		}
	}
}

func TestTreePutEmptyAndOverwrite(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	mustPut(t, tr, "cat", "c")
	mustPut(t, tr, "ant", "a")
	mustPut(t, tr, "cat", "meow") // 覆盖，value 变长
	checkModel(t, tr, map[string]string{"ant": "a", "cat": "meow"}, []string{"", "bee", "zoo"})
}

func TestTreePutCopiesInput(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	key := []byte("ant")
	val := []byte("a")
	if err := tr.put(key, val); err != nil {
		t.Fatal(err)
	}
	// 调用者在 put 返回后复用自己的缓冲区。
	key[0] = 'x'
	val[0] = 'x'
	checkModel(t, tr, map[string]string{"ant": "a"}, []string{"xnt"})
}

// 叶子不分裂时，路径上每层各新建一页；旧根仍是完整的旧版本。
func TestTreePutPathCopy(t *testing.T) {
	store, tr := buildTwoLevelTree(t)
	oldRoot := tr.root
	pagesBefore := store.nextID

	mustPut(t, tr, "cow", "moo") // 路由到 L1（ant ≤ cow < dog）

	if tr.root == oldRoot {
		t.Fatal("put 后根页号没有变化")
	}
	if created := store.nextID - pagesBefore; created != 2 {
		t.Fatalf("新建 %d 页，期望 2（叶子 + 根）", created)
	}
	checkModel(t, tr, map[string]string{"ant": "a", "cat": "c", "cow": "moo", "dog": "d", "fox": "f"}, nil)

	oldTree := &tree{store: store, root: oldRoot}
	checkModel(t, oldTree, map[string]string{"ant": "a", "cat": "c", "dog": "d", "fox": "f"}, []string{"cow"})
}

// 插入比整棵树都小的 key：父目录第一项的 minKey 随之变化。
func TestTreePutSmallerThanAll(t *testing.T) {
	store, tr := buildTwoLevelTree(t)
	mustPut(t, tr, "aaa", "0")
	checkModel(t, tr, map[string]string{"aaa": "0", "ant": "a", "cat": "c", "dog": "d", "fox": "f"}, nil)

	rootPage, err := store.get(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey := string(getKey(rootPage, 0)); firstKey != "aaa" {
		t.Fatalf("根目录第一项 minKey = %q，期望 \"aaa\"", firstKey)
	}
}

// 孩子叶子分裂：根目录增加一项，所有 key 仍可读。
func TestTreePutLeafSplitUnderRoot(t *testing.T) {
	store, tr := buildTwoLevelTree(t)
	model := map[string]string{"ant": "a", "cat": "c", "dog": "d", "fox": "f"}
	bigValue := string(bytes.Repeat([]byte("v"), 900))
	for _, key := range []string{"bat", "bee", "bug", "cod", "cub"} { // 都落在 L1
		mustPut(t, tr, key, bigValue)
		model[key] = bigValue
	}
	rootPage, err := store.get(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	if nodeNKeys(rootPage) <= 2 {
		t.Fatalf("根目录 %d 项，期望叶子分裂后多于 2 项", nodeNKeys(rootPage))
	}
	checkModel(t, tr, model, []string{"aaa", "eel", "zoo"})
}

// checkTreeShape 用 tree.check 检查全树不变量，失败即终止测试，返回树高。
func checkTreeShape(t *testing.T, tr *tree) int {
	t.Helper()
	height, err := tr.check()
	if err != nil {
		t.Fatalf("全树检查失败：%v", err)
	}
	return height
}

type treeSnapshot struct {
	root  uint64
	model map[string]string
}

func takeSnapshot(tr *tree, model map[string]string) treeSnapshot {
	copied := make(map[string]string, len(model))
	for k, v := range model {
		copied[k] = v
	}
	return treeSnapshot{root: tr.root, model: copied}
}

// put 用 if 而非循环处理根分裂，依赖"根最多返回 3 项且 3 项一定装进一页"。
// 若有人放宽 maxKeySize，这里先失败，提醒把根分裂改成循环。
func TestRootSplitEntriesFitInOnePage(t *testing.T) {
	maxEntryBytes := 2 + 4 + maxKeySize + 8 // offset 表项 + klen/vlen + key + 子页号
	if need := headerSize + 3*maxEntryBytes; need > PageSize {
		t.Fatalf("3 个最大目录项需要 %d 字节，超过一页 %d；put 的根分裂需改为循环", need, PageSize)
	}
}

// 根叶子装不下：分裂为两张叶子，新建一张内部页作根，树高 1 → 2；旧根仍是旧版本。
func TestTreePutRootLeafSplit(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	model := map[string]string{}
	bigValue := string(bytes.Repeat([]byte("v"), 1000))
	var before treeSnapshot
	for i := 0; i < 5; i++ { // 第 5 条放不进一张叶子
		before = takeSnapshot(tr, model)
		key := fmt.Sprintf("k%d", i)
		mustPut(t, tr, key, bigValue)
		model[key] = bigValue
	}
	rootPage, err := store.get(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	if nodeType(rootPage) != NodeInternal || nodeNKeys(rootPage) < 2 {
		t.Fatalf("根类型 %d、%d 项，期望内部页且至少 2 项", nodeType(rootPage), nodeNKeys(rootPage))
	}
	if height := checkTreeShape(t, tr); height != 2 {
		t.Fatalf("树高 %d，期望 2", height)
	}
	checkModel(t, tr, model, []string{"k5"})

	oldTree := &tree{store: store, root: before.root}
	if height := checkTreeShape(t, oldTree); height != 1 {
		t.Fatalf("旧根树高 %d，期望 1", height)
	}
	checkModel(t, oldTree, before.model, []string{"k4"})
}

// 从空树开始连续插入：根多次分裂，树高持续增长；每次插入后结构不变量成立，所有快照仍可读。
func TestTreePutGrowsFromEmpty(t *testing.T) {
	cases := []struct {
		name     string
		count    int
		keyLen   int // key 总长度；1000 字节 key 让内部页只能放几项，根会更频繁地分裂
		valueLen int
	}{
		{"短 key", 4000, 8, 200}, // value 较大，叶子多到根目录装不下
		{"1000 字节 key", 300, maxKeySize, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newMemPageStore()
			tr := mustNewTree(t, store)
			model := map[string]string{}
			rng := rand.New(rand.NewSource(2))
			var snapshots []treeSnapshot
			maxHeight := 1
			for op, n := range rng.Perm(c.count) {
				prefix := fmt.Sprintf("%06d", n)
				key := prefix + string(bytes.Repeat([]byte("-"), c.keyLen-len(prefix)))
				value := string(bytes.Repeat([]byte{byte('a' + op%26)}, c.valueLen))
				mustPut(t, tr, key, value)
				model[key] = value
				if height := checkTreeShape(t, tr); height > maxHeight {
					maxHeight = height
					snapshots = append(snapshots, takeSnapshot(tr, model)) // 每次长高时留一个快照
				}
			}
			if maxHeight < 3 {
				t.Fatalf("最大树高 %d，期望至少 3（根至少分裂两次）", maxHeight)
			}
			checkModel(t, tr, model, []string{"", "zzz"})
			for _, snap := range snapshots {
				checkModel(t, &tree{store: store, root: snap.root}, snap.model, nil)
			}
		})
	}
}

// 保存任何页之前就能发现的错误：不新建页、不改根。
func TestTreePutErrorsKeepRoot(t *testing.T) {
	store, tr := buildTwoLevelTree(t)
	rootBefore := tr.root
	pagesBefore := store.nextID

	longKey := bytes.Repeat([]byte("k"), maxKeySize+1)
	if err := tr.put(longKey, []byte("v")); !errors.Is(err, ErrKeyTooLarge) {
		t.Fatalf("超长 key：err = %v，期望 ErrKeyTooLarge", err)
	}
	hugeValue := make([]byte, PageSize)
	if err := tr.put([]byte("cow"), hugeValue); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("超大 value：err = %v，期望 ErrRecordTooLarge", err)
	}
	if tr.root != rootBefore || store.nextID != pagesBefore {
		t.Fatalf("失败的 put 改变了状态：root %d→%d，nextID %d→%d", rootBefore, tr.root, pagesBefore, store.nextID)
	}

	// 下降途中缺页：同样在保存之前失败。
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), 99}})
	rootBefore = tr.root
	if err := tr.put([]byte("ant"), []byte("a")); !errors.Is(err, ErrPageNotFound) || tr.root != rootBefore {
		t.Fatalf("缺页：err = %v，root 是否改变 %v", err, tr.root != rootBefore)
	}
}

// 下降途中遇到环：put 也必须报 ErrCorruptTree，而不是无限递归。
func TestTreePutCycle(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	rootID := store.nextID
	childID := rootID + 1
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), childID}})
	saveInternalPage(t, store, []internalEntry{{[]byte("ant"), rootID}}) // 孩子指回根
	rootBefore := tr.root
	if err := tr.put([]byte("ant"), []byte("a")); !errors.Is(err, ErrCorruptTree) || tr.root != rootBefore {
		t.Fatalf("err = %v，root 是否改变 %v；期望 ErrCorruptTree 且根不变", err, tr.root != rootBefore)
	}
}

// 随机插入/覆盖，与参考 map 对照；中途留一个旧根快照，最后检查它仍是当时的版本。
func TestTreePutRandomAgainstMap(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	// 预先搭两张叶子，让根一开始就是内部页；本知识点还不能分裂根。
	leafLow := saveLeafPage(t, store, [][2]string{{"k0000", "init-low"}})
	leafHigh := saveLeafPage(t, store, [][2]string{{"k5000", "init-high"}})
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("k0000"), leafLow}, {[]byte("k5000"), leafHigh}})
	model := map[string]string{"k0000": "init-low", "k5000": "init-high"}

	rng := rand.New(rand.NewSource(1))
	var snapshotRoot uint64
	var snapshotModel map[string]string
	for op := 0; op < 1000; op++ {
		key := fmt.Sprintf("k%04d", rng.Intn(600)*16) // 600 个候选 key，必然有覆盖
		valueLen := 5 + rng.Intn(60)
		value := string(bytes.Repeat([]byte{byte('a' + op%26)}, valueLen))
		mustPut(t, tr, key, value)
		model[key] = value
		if op == 300 {
			snapshotRoot = tr.root
			snapshotModel = make(map[string]string, len(model))
			for k, v := range model {
				snapshotModel[k] = v
			}
		}
	}
	checkModel(t, tr, model, []string{"", "j", "k0001", "k9999", "z"})

	snapshot := &tree{store: store, root: snapshotRoot}
	var snapshotAbsent []string
	for key := range model {
		if _, ok := snapshotModel[key]; !ok {
			snapshotAbsent = append(snapshotAbsent, key)
		}
	}
	sort.Strings(snapshotAbsent)
	checkModel(t, snapshot, snapshotModel, snapshotAbsent)
}

func mustDelete(t *testing.T, tr *tree, key string, wantFound bool) {
	t.Helper()
	found, err := tr.delete([]byte(key))
	if err != nil {
		t.Fatalf("delete(%q) 出错：%v", key, err)
	}
	if found != wantFound {
		t.Fatalf("delete(%q) found = %v，期望 %v", key, found, wantFound)
	}
}

func rootPageOf(t *testing.T, tr *tree) []byte {
	t.Helper()
	page, err := tr.store.get(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// 根是叶子：删除一条、删除不存在的 key、一直删到空。
func TestTreeDeleteLeafRoot(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	mustPut(t, tr, "ant", "a")
	mustPut(t, tr, "cat", "c")

	mustDelete(t, tr, "ant", true)
	checkModel(t, tr, map[string]string{"cat": "c"}, []string{"ant"})

	rootBefore, pagesBefore := tr.root, store.nextID
	mustDelete(t, tr, "zzz", false)
	if tr.root != rootBefore || store.nextID != pagesBefore {
		t.Fatal("删除不存在的 key 改变了树或新建了页")
	}

	mustDelete(t, tr, "cat", true)
	root := rootPageOf(t, tr)
	if nodeType(root) != NodeLeaf || nodeNKeys(root) != 0 {
		t.Fatalf("删空后根类型 %d、%d 条，期望空叶子", nodeType(root), nodeNKeys(root))
	}
	checkModel(t, tr, map[string]string{}, []string{"ant", "cat"})
	mustDelete(t, tr, "cat", false) // 空树上再删
}

// 两层树中删除：不存在时不复制路径；存在时路径复制，旧根仍是旧版本。
func TestTreeDeletePathCopy(t *testing.T) {
	store, tr := buildTwoLevelTree(t) // root [ant→{ant,cat}, dog→{dog,fox}]
	rootBefore, pagesBefore := tr.root, store.nextID
	mustDelete(t, tr, "cow", false)
	if tr.root != rootBefore || store.nextID != pagesBefore {
		t.Fatal("删除不存在的 key 复制了路径或发布了新根")
	}

	mustDelete(t, tr, "cat", true)
	if created := store.nextID - pagesBefore; created != 2 {
		t.Fatalf("新建 %d 页，期望 2（叶子 + 根）", created)
	}
	checkTreeShape(t, tr)
	checkModel(t, tr, map[string]string{"ant": "a", "dog": "d", "fox": "f"}, []string{"cat"})
	oldTree := &tree{store: store, root: rootBefore}
	checkModel(t, oldTree, map[string]string{"ant": "a", "cat": "c", "dog": "d", "fox": "f"}, nil)
}

// 删除子树最小的 key：父目录 minKey 随之更新（checkTreeShape 会检查）。
func TestTreeDeleteMinKeyUpdatesParent(t *testing.T) {
	_, tr := buildTwoLevelTree(t)
	mustDelete(t, tr, "dog", true)
	checkTreeShape(t, tr)
	if minKey := string(getKey(rootPageOf(t, tr), 1)); minKey != "fox" {
		t.Fatalf("根目录第 1 项 minKey = %q，期望 \"fox\"", minKey)
	}
}

// 叶子删空：从父页摘掉；父页还有多个孩子时不收缩。
func TestTreeDeleteEmptiedLeafRemoved(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	leafA := saveLeafPage(t, store, [][2]string{{"ant", "a"}})
	leafD := saveLeafPage(t, store, [][2]string{{"dog", "d"}})
	leafP := saveLeafPage(t, store, [][2]string{{"pig", "p"}})
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), leafA}, {[]byte("dog"), leafD}, {[]byte("pig"), leafP}})

	mustDelete(t, tr, "dog", true)
	root := rootPageOf(t, tr)
	if nodeType(root) != NodeInternal || nodeNKeys(root) != 2 {
		t.Fatalf("根类型 %d、%d 项，期望内部页 2 项", nodeType(root), nodeNKeys(root))
	}
	checkTreeShape(t, tr) // 非根叶子不能为空
	checkModel(t, tr, map[string]string{"ant": "a", "pig": "p"}, []string{"dog"})
}

// 根只剩一个孩子：提升孩子，树高 2 → 1。
func TestTreeDeleteRootShrink(t *testing.T) {
	_, tr := buildTwoLevelTree(t)
	mustDelete(t, tr, "dog", true)
	mustDelete(t, tr, "fox", true) // L2 删空 → 根只剩 L1 → L1 当根
	if height := checkTreeShape(t, tr); height != 1 {
		t.Fatalf("树高 %d，期望 1", height)
	}
	checkModel(t, tr, map[string]string{"ant": "a", "cat": "c"}, []string{"dog", "fox"})
}

// 连锁：左子树整棵删空 → 左内部页也被摘掉 → 根只剩右子树 → 收缩，树高 3 → 2。
func TestTreeDeleteCascade(t *testing.T) {
	store := newMemPageStore()
	leaf1 := saveLeafPage(t, store, [][2]string{{"a1", "1"}, {"a2", "2"}})
	leaf2 := saveLeafPage(t, store, [][2]string{{"b1", "3"}, {"b2", "4"}})
	leaf3 := saveLeafPage(t, store, [][2]string{{"c1", "5"}, {"c2", "6"}})
	leaf4 := saveLeafPage(t, store, [][2]string{{"d1", "7"}, {"d2", "8"}})
	left := saveInternalPage(t, store, []internalEntry{{[]byte("a1"), leaf1}, {[]byte("b1"), leaf2}})
	right := saveInternalPage(t, store, []internalEntry{{[]byte("c1"), leaf3}, {[]byte("d1"), leaf4}})
	tr := mustNewTree(t, store)
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a1"), left}, {[]byte("c1"), right}})

	for _, key := range []string{"a1", "a2", "b1", "b2"} {
		mustDelete(t, tr, key, true)
		checkTreeShape(t, tr)
	}
	if height := checkTreeShape(t, tr); height != 2 {
		t.Fatalf("树高 %d，期望 2", height)
	}
	checkModel(t, tr, map[string]string{"c1": "5", "c2": "6", "d1": "7", "d2": "8"}, []string{"a1", "b2"})
}

// 删除也可能让父页分裂：删掉最小 key 后，新的最小 key 是 1000 字节长 key，父目录项变长装不下。
func TestTreeDeleteCanSplitParent(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	longKey := "k100" + string(bytes.Repeat([]byte("x"), maxKeySize-4)) // 介于 k100 与 k101 之间
	var entries []internalEntry
	model := map[string]string{}
	for i := 0; i < 200; i++ { // 200 项短目录，每项 18 字节，根约装 3612 字节
		key := fmt.Sprintf("k%03d", i)
		pairs := [][2]string{{key, "v"}}
		model[key] = "v"
		if i == 100 {
			pairs = append(pairs, [2]string{longKey, "long"})
			model[longKey] = "long"
		}
		entries = append(entries, internalEntry{[]byte(key), saveLeafPage(t, store, pairs)})
	}
	tr.root = saveInternalPage(t, store, entries)

	mustDelete(t, tr, "k100", true) // 目录项 k100 → longKey，多出约 1000 字节
	delete(model, "k100")
	if height := checkTreeShape(t, tr); height != 3 {
		t.Fatalf("树高 %d，期望 3（根因目录项变长而分裂）", height)
	}
	checkModel(t, tr, model, []string{"k100"})
}

// 随机插入与删除混合，每步检查结构，与参考 map、游标扫描和快照对照，最后全部删空。
func TestTreeDeleteRandomAgainstMap(t *testing.T) {
	store := newMemPageStore()
	tr := mustNewTree(t, store)
	model := map[string]string{}
	rng := rand.New(rand.NewSource(4))
	var snapshots []treeSnapshot
	for op := 0; op < 8000; op++ {
		key := fmt.Sprintf("k%04d", rng.Intn(1500))
		if rng.Intn(100) < 60 { // 先偏向插入，让树长到多层
			value := string(bytes.Repeat([]byte{byte('a' + op%26)}, 100+rng.Intn(150)))
			mustPut(t, tr, key, value)
			model[key] = value
		} else {
			_, exists := model[key]
			mustDelete(t, tr, key, exists)
			delete(model, key)
		}
		checkTreeShape(t, tr)
		if op%1000 == 999 {
			snapshots = append(snapshots, takeSnapshot(tr, model))
		}
	}
	checkModel(t, tr, model, []string{"", "zzz"})
	checkRange(t, collectRange(t, tr.newCursor(), nil, nil), expectedRange(model, nil, nil), "游标全表")
	for _, snap := range snapshots {
		checkModel(t, &tree{store: store, root: snap.root}, snap.model, nil)
	}

	// 全部删空：树回到一张空叶子根。
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for _, key := range keys {
		mustDelete(t, tr, key, true)
		checkTreeShape(t, tr)
	}
	root := rootPageOf(t, tr)
	if nodeType(root) != NodeLeaf || nodeNKeys(root) != 0 {
		t.Fatalf("全部删除后根类型 %d、%d 条，期望空叶子", nodeType(root), nodeNKeys(root))
	}
}
