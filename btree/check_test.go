package btree

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckHealthyTrees(t *testing.T) {
	tr := mustNewTree(t, newMemPageStore())
	if height, err := tr.check(); err != nil || height != 1 {
		t.Fatalf("空树：height %d, err %v", height, err)
	}
	_, twoLevel := buildTwoLevelTree(t)
	if height, err := twoLevel.check(); err != nil || height != 2 {
		t.Fatalf("两层树：height %d, err %v", height, err)
	}
}

// 每种损坏各造一棵树，check 都要报 ErrCorruptTree，且原因可辨认。
func TestCheckDetectsCorruption(t *testing.T) {
	cases := []struct {
		name   string
		build  func(t *testing.T, store *memPageStore) uint64 // 返回根页号
		reason string
	}{
		{"两个目录项指向同一页", func(t *testing.T, store *memPageStore) uint64 {
			leaf := saveLeafPage(t, store, [][2]string{{"ant", "a"}})
			return saveInternalPage(t, store, []internalEntry{{[]byte("ant"), leaf}, {[]byte("bee"), leaf}})
		}, "referenced more than once"},
		{"minKey 过时", func(t *testing.T, store *memPageStore) uint64 {
			l1 := saveLeafPage(t, store, [][2]string{{"ant", "a"}})
			l2 := saveLeafPage(t, store, [][2]string{{"dog", "d"}})
			return saveInternalPage(t, store, []internalEntry{{[]byte("ant"), l1}, {[]byte("cow"), l2}})
		}, "child subtree min"},
		{"key 越过右边界", func(t *testing.T, store *memPageStore) uint64 {
			l1 := saveLeafPage(t, store, [][2]string{{"ant", "a"}, {"dog", "d"}}) // dog 应在右边子树
			l2 := saveLeafPage(t, store, [][2]string{{"cow", "c"}, {"fox", "f"}})
			return saveInternalPage(t, store, []internalEntry{{[]byte("ant"), l1}, {[]byte("cow"), l2}})
		}, "outside parent range"},
		{"叶子深度不同", func(t *testing.T, store *memPageStore) uint64 {
			la := saveLeafPage(t, store, [][2]string{{"a1", "1"}})
			lc := saveLeafPage(t, store, [][2]string{{"c1", "3"}})
			mid := saveInternalPage(t, store, []internalEntry{{[]byte("c1"), lc}})
			return saveInternalPage(t, store, []internalEntry{{[]byte("a1"), la}, {[]byte("c1"), mid}})
		}, "leaf depth"},
		{"非根空叶子", func(t *testing.T, store *memPageStore) uint64 {
			la := saveLeafPage(t, store, [][2]string{{"a1", "1"}})
			empty := saveLeafPage(t, store, nil)
			return saveInternalPage(t, store, []internalEntry{{[]byte("a1"), la}, {[]byte("m"), empty}})
		}, "non-root leaf is empty"},
		{"sibling 非 0", func(t *testing.T, store *memPageStore) uint64 {
			id, err := store.save(leafBuild([]leafRecord{{[]byte("ant"), []byte("a")}}, 7))
			if err != nil {
				t.Fatal(err)
			}
			return id
		}, "sibling is not 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemPageStore()
			tr := mustNewTree(t, store)
			tr.root = tc.build(t, store)
			_, err := tr.check()
			if !errors.Is(err, ErrCorruptTree) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v，期望 ErrCorruptTree 且包含 %q", err, tc.reason)
			}
		})
	}

	t.Run("缺页", func(t *testing.T) {
		store := newMemPageStore()
		tr := mustNewTree(t, store)
		tr.root = saveInternalPage(t, store, []internalEntry{{[]byte("ant"), 99}})
		if _, err := tr.check(); !errors.Is(err, ErrPageNotFound) {
			t.Fatalf("err = %v，期望 ErrPageNotFound", err)
		}
	})
}
