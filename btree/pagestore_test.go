package btree

import (
	"bytes"
	"errors"
	"testing"
)

func TestMemPageStoreSaveAndGet(t *testing.T) {
	store := newMemPageStore()
	leafA := buildLeaf([][2]string{{"ant", "a"}, {"cat", "c"}})
	leafB := buildLeaf([][2]string{{"dog", "d"}})

	idA, err := store.save(leafA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := store.save(leafB)
	if err != nil {
		t.Fatal(err)
	}
	// 页号从 1 开始递增，0 永远不分配。
	if idA != 1 || idB != 2 {
		t.Fatalf("页号 = %d, %d，期望 1, 2", idA, idB)
	}

	gotA, err := store.get(idA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotA, leafA) {
		t.Fatal("读回的页 A 与保存时不一致")
	}
	// 读到的页可以直接交给页函数使用。
	value, found := leafGet(gotA, []byte("cat"))
	if !found || string(value) != "c" {
		t.Fatalf("leafGet(cat) = %q, %v", value, found)
	}
	gotB, err := store.get(idB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotB, leafB) {
		t.Fatal("读回的页 B 与保存时不一致")
	}
}

func TestMemPageStoreSaveCopiesInput(t *testing.T) {
	store := newMemPageStore()
	page := buildLeaf([][2]string{{"ant", "a"}})
	before := bytes.Clone(page)

	id, err := store.save(page)
	if err != nil {
		t.Fatal(err)
	}
	// 调用者在保存后复用自己的缓冲区。
	for i := range page {
		page[i] = 0xFF
	}

	got, err := store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, before) {
		t.Fatal("保存后修改输入，影响了已保存的页")
	}
}

func TestMemPageStoreCoWKeepsOldPage(t *testing.T) {
	store := newMemPageStore()
	oldLeaf := buildLeaf([][2]string{{"ant", "a"}, {"cat", "c"}})
	oldID, err := store.save(oldLeaf)
	if err != nil {
		t.Fatal(err)
	}

	// CoW：读旧页 → 重建新字节 → 保存为新页号。
	oldView, err := store.get(oldID)
	if err != nil {
		t.Fatal(err)
	}
	newLeaf, err := leafInsert(oldView, []byte("cat"), []byte("meow"))
	if err != nil {
		t.Fatal(err)
	}
	newID, err := store.save(newLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatal("新版本复用了旧页号")
	}

	oldAgain, err := store.get(oldID)
	if err != nil {
		t.Fatal(err)
	}
	oldValue, _ := leafGet(oldAgain, []byte("cat"))
	if string(oldValue) != "c" {
		t.Fatalf("旧页被改变：cat = %q", oldValue)
	}
	newAgain, err := store.get(newID)
	if err != nil {
		t.Fatal(err)
	}
	newValue, _ := leafGet(newAgain, []byte("cat"))
	if string(newValue) != "meow" {
		t.Fatalf("新页内容错误：cat = %q", newValue)
	}
}

func TestMemPageStoreErrors(t *testing.T) {
	store := newMemPageStore()

	badSizes := []int{0, 1, PageSize - 1, PageSize + 1}
	for _, size := range badSizes {
		id, err := store.save(make([]byte, size))
		if !errors.Is(err, ErrBadPageSize) || id != 0 {
			t.Fatalf("save(%d 字节) = %d, %v，期望 0, ErrBadPageSize", size, id, err)
		}
	}
	// 失败的 save 不消耗页号：下一次成功保存仍得到 1。
	id, err := store.save(make([]byte, PageSize))
	if err != nil || id != 1 {
		t.Fatalf("失败后首次成功 save = %d, %v，期望 1, nil", id, err)
	}

	missingIDs := []uint64{0, 2, 1 << 40}
	for _, missing := range missingIDs {
		page, err := store.get(missing)
		if !errors.Is(err, ErrPageNotFound) || page != nil {
			t.Fatalf("get(%d) = %v, %v，期望 nil, ErrPageNotFound", missing, page, err)
		}
	}
}
