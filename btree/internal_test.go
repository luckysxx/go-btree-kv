package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestInternalRouting(t *testing.T) {
	// 非连续页号，以及大于 uint32 的编号：目录按 key 排序，而非按页号排序。
	const rightPageID uint64 = 1<<40 + 31
	leaves := map[uint64][]byte{
		10:          buildLeaf([][2]string{{"ant", "a"}, {"cat", "c"}}),
		25:          buildLeaf([][2]string{{"dog", "d"}, {"fox", "f"}}),
		rightPageID: buildLeaf([][2]string{{"pig", "p"}, {"zoo", "z"}}),
	}
	entries := []internalEntry{{[]byte("ant"), 10}, {[]byte("dog"), 25}, {[]byte("pig"), rightPageID}}
	page, err := internalBuild(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != PageSize || nodeType(page) != NodeInternal || nodeNKeys(page) != 3 || nodeSibling(page) != 0 || nodeUsedBytes(page) != 63 {
		t.Fatal("内部页布局不正确")
	}
	for entryIndex, entry := range entries {
		if !bytes.Equal(getKey(page, entryIndex), entry.minKey) || len(getValue(page, entryIndex)) != 8 ||
			binary.LittleEndian.Uint64(getValue(page, entryIndex)) != entry.childPageID {
			t.Fatalf("目录记录 %d 编解码不一致", entryIndex)
		}
	}
	before := bytes.Clone(page)
	cases := []struct {
		key       string
		wantChild uint64
		wantValue string
		wantFound bool
	}{
		{"", 10, "", false},
		{"aardvark", 10, "", false},
		{"ant", 10, "a", true},
		{"cat", 10, "c", true},
		{"dog", 25, "d", true},
		{"eel", 25, "", false},
		{"fox", 25, "f", true},
		{"pig", rightPageID, "p", true},
		{"zoo", rightPageID, "z", true},
		{"zzz", rightPageID, "", false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			childID := internalChild(page, []byte(c.key))
			if childID != c.wantChild {
				t.Fatalf("child=%d, want %d", childID, c.wantChild)
			}
			value, found := leafGet(leaves[childID], []byte(c.key))
			if found != c.wantFound || string(value) != c.wantValue {
				t.Fatalf("经目录查叶子=(%q,%v), want (%q,%v)", value, found, c.wantValue, c.wantFound)
			}
		})
	}
	if !bytes.Equal(page, before) {
		t.Fatal("路由修改了目录页")
	}
	for _, entry := range entries {
		clear(entry.minKey)
	}
	if !bytes.Equal(page, before) {
		t.Fatal("结果页仍借用输入 key 的存储")
	}
}

func TestInternalBuildBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		entries []internalEntry
		wantErr error
	}{
		{"空目录", nil, ErrInvalidInternalEntries},
		{"零页号", []internalEntry{{[]byte("a"), 0}}, ErrInvalidInternalEntries},
		{"重复key", []internalEntry{{[]byte("a"), 1}, {[]byte("a"), 2}}, ErrInvalidInternalEntries},
		{"逆序key", []internalEntry{{[]byte("b"), 1}, {[]byte("a"), 2}}, ErrInvalidInternalEntries},
		{"空key合法", []internalEntry{{nil, 1}, {[]byte("a"), 2}}, nil},
		{"单条恰好装满", []internalEntry{{bytes.Repeat([]byte("a"), 4070), 1}}, nil},
		{"单条超一字节", []internalEntry{{bytes.Repeat([]byte("a"), 4071), 1}}, ErrPageFull},
		{"两条恰好装满", []internalEntry{{bytes.Repeat([]byte("a"), 2028), 1}, {bytes.Repeat([]byte("b"), 2028), 2}}, nil},
		{"两条超一字节", []internalEntry{{bytes.Repeat([]byte("a"), 2028), 1}, {bytes.Repeat([]byte("b"), 2029), 2}}, ErrPageFull},
		{"超长key", []internalEntry{{make([]byte, 65536), 1}}, ErrPageFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, err := internalBuild(c.entries)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err=%v, want %v", err, c.wantErr)
			}
			if err != nil {
				if page != nil {
					t.Fatal("失败返回了半成品页")
				}
				return
			}
			usedBytes := headerSize
			for _, entry := range c.entries {
				usedBytes += 2 + 4 + len(entry.minKey) + 8
				if got := internalChild(page, entry.minKey); got != entry.childPageID {
					t.Fatalf("边界 key 的孩子=%d, want %d", got, entry.childPageID)
				}
			}
			if nodeUsedBytes(page) != usedBytes || usedBytes > PageSize {
				t.Fatalf("内部页占用=%d, want %d", nodeUsedBytes(page), usedBytes)
			}
			if len(c.entries) == 1 {
				for _, key := range [][]byte{nil, []byte("z")} {
					if internalChild(page, key) != c.entries[0].childPageID {
						t.Fatal("单孩子目录应始终选择该孩子")
					}
				}
			}
		})
	}
}
