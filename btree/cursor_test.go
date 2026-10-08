package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// collectRange 用游标读出 [start, end) 内的全部记录；start 为 nil 时从头开始，end 为 nil 时读到末尾。
func collectRange(t *testing.T, c *cursor, start, end []byte) [][2]string {
	t.Helper()
	var err error
	if start == nil {
		err = c.first()
	} else {
		err = c.seek(start)
	}
	if err != nil {
		t.Fatalf("定位出错：%v", err)
	}
	var got [][2]string
	for c.valid() {
		if end != nil && bytes.Compare(c.key(), end) >= 0 {
			break
		}
		got = append(got, [2]string{string(c.key()), string(c.value())})
		if err := c.next(); err != nil {
			t.Fatalf("next 出错：%v", err)
		}
	}
	return got
}

// expectedRange 从参考 map 中按 key 排序取出 [start, end) 的记录。
func expectedRange(model map[string]string, start, end []byte) [][2]string {
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var want [][2]string
	for _, key := range keys {
		if start != nil && key < string(start) {
			continue
		}
		if end != nil && key >= string(end) {
			break
		}
		want = append(want, [2]string{key, model[key]})
	}
	return want
}

func checkRange(t *testing.T, got, want [][2]string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：得到 %d 条，期望 %d 条\n得到 %v\n期望 %v", label, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s：第 %d 条 = %v，期望 %v", label, i, got[i], want[i])
		}
	}
}

func TestCursorEmptyTree(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	c := tr.newCursor()
	if got := collectRange(t, c, nil, nil); len(got) != 0 {
		t.Fatalf("空树扫描得到 %v", got)
	}
	if got := collectRange(t, c, []byte("ant"), nil); len(got) != 0 {
		t.Fatalf("空树 seek 得到 %v", got)
	}
}

// 两层树 root [ant→L1{ant,cat}, dog→L2{dog,fox}]：跨叶、叶间空隙与首尾边界。
func TestCursorTwoLevels(t *testing.T) {
	_, tr := buildTwoLevelTree(t)
	model := map[string]string{"ant": "a", "cat": "c", "dog": "d", "fox": "f"}
	cases := []struct {
		start, end string // 空串表示不限
	}{
		{"", ""},
		{"ant", ""},
		{"aaa", ""},    // 小于所有 key
		{"cat", ""},    // 叶子最后一条
		{"cow", ""},    // 落在两张叶子之间：L1 读完后要走到 L2
		{"dog", "fox"}, // 右开：不含 fox
		{"bee", "eel"}, // 跨叶的中间一段
		{"zzz", ""},    // 大于所有 key
		{"cat", "cat"}, // 空范围
	}
	for _, tc := range cases {
		var start, end []byte
		if tc.start != "" {
			start = []byte(tc.start)
		}
		if tc.end != "" {
			end = []byte(tc.end)
		}
		got := collectRange(t, tr.newCursor(), start, end)
		checkRange(t, got, expectedRange(model, start, end), fmt.Sprintf("[%q, %q)", tc.start, tc.end))
	}
}

// 三层树：左子树读完后要回到根，再走右子树的最左叶子。
func TestCursorThreeLevels(t *testing.T) {
	store := newMemPageStore()
	leaf1 := saveLeafPage(t, store, [][2]string{{"a1", "1"}, {"a2", "2"}})
	leaf2 := saveLeafPage(t, store, [][2]string{{"b1", "3"}, {"b2", "4"}})
	leaf3 := saveLeafPage(t, store, [][2]string{{"c1", "5"}, {"c2", "6"}})
	leaf4 := saveLeafPage(t, store, [][2]string{{"d1", "7"}, {"d2", "8"}})
	left := saveInternalPage(t, store, []internalEntry{{[]byte("a1"), leaf1}, {[]byte("b1"), leaf2}})
	right := saveInternalPage(t, store, []internalEntry{{[]byte("c1"), leaf3}, {[]byte("d1"), leaf4}})
	tr := mustNewTree(t, store)
	tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a1"), left}, {[]byte("c1"), right}})
	model := map[string]string{"a1": "1", "a2": "2", "b1": "3", "b2": "4", "c1": "5", "c2": "6", "d1": "7", "d2": "8"}

	checkRange(t, collectRange(t, tr.newCursor(), nil, nil), expectedRange(model, nil, nil), "全表")
	checkRange(t, collectRange(t, tr.newCursor(), []byte("b3"), nil), expectedRange(model, []byte("b3"), nil), "从左子树末尾之后开始")
	checkRange(t, collectRange(t, tr.newCursor(), []byte("a2"), []byte("d1")), expectedRange(model, []byte("a2"), []byte("d1")), "跨两棵子树")
}

// 中间有一张空叶子（D06 删除后可能出现）：扫描要跳过它，而不是停下。
func TestCursorSkipsEmptyLeaf(t *testing.T) {
	store := newMemPageStore()
	leafA := saveLeafPage(t, store, [][2]string{{"a1", "1"}})
	leafEmpty := saveLeafPage(t, store, nil)
	leafEmpty2 := saveLeafPage(t, store, nil)
	leafN := saveLeafPage(t, store, [][2]string{{"n1", "2"}})
	tr := mustNewTree(t, store)
	tr.root = saveInternalPage(t, store, []internalEntry{
		{[]byte("a1"), leafA}, {[]byte("m"), leafEmpty}, {[]byte("m2"), leafEmpty2}, {[]byte("n1"), leafN},
	})
	model := map[string]string{"a1": "1", "n1": "2"}
	checkRange(t, collectRange(t, tr.newCursor(), nil, nil), expectedRange(model, nil, nil), "全表")
	checkRange(t, collectRange(t, tr.newCursor(), []byte("m"), nil), expectedRange(model, []byte("m"), nil), "seek 落在空叶子")
}

// 从空树 put 出的多层树：全表与随机区间都与排序后的参考 map 一致。
func TestCursorAgainstModel(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	model := map[string]string{}
	rng := rand.New(rand.NewSource(3))
	for _, n := range rng.Perm(3000) {
		key := fmt.Sprintf("k%05d", n*3)                                   // 间隔 3，便于构造落在空隙里的边界
		value := string(bytes.Repeat([]byte{byte('a' + n%26)}, 150+n%100)) // 较大的 value 让叶子多到根装不下
		mustPut(t, tr, key, value)
		model[key] = value
	}
	if height := checkTreeShape(t, tr); height < 3 {
		t.Fatalf("树高 %d，期望至少 3，才能覆盖多层回退", height)
	}
	checkRange(t, collectRange(t, tr.newCursor(), nil, nil), expectedRange(model, nil, nil), "全表")
	for i := 0; i < 300; i++ {
		a := fmt.Sprintf("k%05d", rng.Intn(9100))
		b := fmt.Sprintf("k%05d", rng.Intn(9100))
		if a > b {
			a, b = b, a
		}
		got := collectRange(t, tr.newCursor(), []byte(a), []byte(b))
		checkRange(t, got, expectedRange(model, []byte(a), []byte(b)), fmt.Sprintf("[%s, %s)", a, b))
	}
}

// 扫描途中 put 并发布新根：已有游标仍读旧版本，新游标读新版本。
func TestCursorSnapshot(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	oldModel := map[string]string{}
	for i := 0; i < 2000; i++ {
		key := fmt.Sprintf("k%05d", i*2)
		value := fmt.Sprintf("old-%d", i)
		mustPut(t, tr, key, value)
		oldModel[key] = value
	}
	c := tr.newCursor()
	if err := c.first(); err != nil {
		t.Fatal(err)
	}
	var got [][2]string
	for i := 0; i < 500 && c.valid(); i++ { // 先读一部分
		got = append(got, [2]string{string(c.key()), string(c.value())})
		if err := c.next(); err != nil {
			t.Fatal(err)
		}
	}

	// 途中修改：前后都插入新 key，并覆盖若干旧 key，触发多次分裂。
	newModel := map[string]string{}
	for k, v := range oldModel {
		newModel[k] = v
	}
	for i := 0; i < 2000; i++ {
		key := fmt.Sprintf("k%05d", i*2+1)
		mustPut(t, tr, key, "new")
		newModel[key] = "new"
	}
	for i := 0; i < 4000; i += 7 {
		key := fmt.Sprintf("k%05d", i)
		mustPut(t, tr, key, "overwritten")
		newModel[key] = "overwritten"
	}

	for c.valid() {
		got = append(got, [2]string{string(c.key()), string(c.value())})
		if err := c.next(); err != nil {
			t.Fatal(err)
		}
	}
	checkRange(t, got, expectedRange(oldModel, nil, nil), "旧游标")
	checkRange(t, collectRange(t, tr.newCursor(), nil, nil), expectedRange(newModel, nil, nil), "新游标")
}

func TestCursorCorruption(t *testing.T) {
	t.Run("定位时缺页", func(t *testing.T) {
		store := newMemPageStore()
		tr := mustNewTree(t, store)
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a"), 99}})
		c := tr.newCursor()
		if err := c.first(); !errors.Is(err, ErrPageNotFound) || c.valid() {
			t.Fatalf("err = %v, valid = %v，期望 ErrPageNotFound 且无效", err, c.valid())
		}
	})

	t.Run("换叶时缺页", func(t *testing.T) {
		store := newMemPageStore()
		leafA := saveLeafPage(t, store, [][2]string{{"a1", "1"}})
		tr := mustNewTree(t, store)
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a1"), leafA}, {[]byte("m"), 99}})
		c := tr.newCursor()
		if err := c.first(); err != nil || !c.valid() || string(c.key()) != "a1" {
			t.Fatalf("first：err = %v，期望停在 a1", err)
		}
		if err := c.next(); !errors.Is(err, ErrPageNotFound) || c.valid() {
			t.Fatalf("next：err = %v, valid = %v，期望 ErrPageNotFound 且无效", err, c.valid())
		}
	})

	t.Run("换叶时遇到环", func(t *testing.T) {
		store := newMemPageStore()
		leafA := saveLeafPage(t, store, [][2]string{{"a1", "1"}})
		tr := mustNewTree(t, store)
		rootID := store.nextID
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("a1"), leafA}, {[]byte("m"), rootID}}) // 第二个孩子指回根
		c := tr.newCursor()
		if err := c.first(); err != nil {
			t.Fatal(err)
		}
		if err := c.next(); !errors.Is(err, ErrCorruptTree) || c.valid() {
			t.Fatalf("next：err = %v, valid = %v，期望 ErrCorruptTree 且无效", err, c.valid())
		}
	})
}
