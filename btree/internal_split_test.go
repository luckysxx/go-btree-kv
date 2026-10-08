package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// 每个 key 的前两字节给出有序编号，后面用相同字节填充到指定长度。
// 测试调用方保证长度至少为 2、目录项不超过 uint16 编号范围。
func internalEntriesWithKeyLengths(keyLengths ...int) []internalEntry {
	entries := make([]internalEntry, len(keyLengths))
	for entryIndex, keyLength := range keyLengths {
		key := bytes.Repeat([]byte{'k'}, keyLength)
		binary.BigEndian.PutUint16(key, uint16(entryIndex+1))
		entries[entryIndex] = internalEntry{key, 1<<40 + uint64(3*entryIndex+1)}
	}
	return entries
}

func TestInternalBuildSplit(t *testing.T) {
	manyKeyLengths := make([]int, 256)
	for entryIndex := range manyKeyLengths {
		manyKeyLengths[entryIndex] = 2
	}
	cases := []struct {
		name       string
		entries    []internalEntry
		wantCounts []int
	}{
		{"单个空key", []internalEntry{{nil, 1 << 40}}, []int{1}},
		{"空key和二进制key", []internalEntry{{nil, 10}, {[]byte{0}, 25}, {[]byte{0xff, 0}, 31}}, []int{3}},
		{"单key恰好上限", internalEntriesWithKeyLengths(1000), []int{1}},
		{"四个最长key仍在一页", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000), []int{4}},
		{"单页恰好4096字节", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 14), []int{5}},
		{"超一字节并调整尾组", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 15), []int{3, 2}},
		{"五个最长key调整为三加二", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000), []int{3, 2}},
		{"六项按字节先装满左页", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000), []int{4, 2}},
		{"两页无需调整", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000), []int{4, 4}},
		{"三页只调整最后一个分界", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000), []int{4, 3, 2}},
		{"三页无需调整", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000), []int{4, 4, 4}},
		{"不按条数对半分", internalEntriesWithKeyLengths(1000, 1000, 1000, 20, 20, 1000, 20, 20), []int{5, 3}},
		{"多条短key的尾组调整", internalEntriesWithKeyLengths(manyKeyLengths...), []int{254, 2}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := make([]internalEntry, len(testCase.entries))
			for entryIndex, entry := range testCase.entries {
				before[entryIndex] = internalEntry{bytes.Clone(entry.minKey), entry.childPageID}
			}
			pages, err := internalBuildSplit(testCase.entries)
			if err != nil || len(pages) != len(testCase.wantCounts) {
				t.Fatalf("pages=%d err=%v, want %d pages", len(pages), err, len(testCase.wantCounts))
			}
			for entryIndex, entry := range testCase.entries {
				if !bytes.Equal(entry.minKey, before[entryIndex].minKey) || entry.childPageID != before[entryIndex].childPageID {
					t.Fatal("构建修改了输入")
				}
			}
			entryIndex := 0
			parentEntries := make([]internalEntry, len(pages))
			for pageIndex, page := range pages {
				if len(page) != PageSize || nodeType(page) != NodeInternal || nodeSibling(page) != 0 || int(nodeNKeys(page)) != testCase.wantCounts[pageIndex] {
					t.Fatalf("第 %d 页长度、类型、sibling 或条数错误", pageIndex)
				}
				if len(pages) > 1 && nodeNKeys(page) < 2 {
					t.Fatal("分裂结果含单孩子内部页")
				}
				wantUsedBytes := headerSize
				for localIndex := 0; localIndex < int(nodeNKeys(page)); localIndex++ {
					want := before[entryIndex]
					if !bytes.Equal(getKey(page, localIndex), want.minKey) || len(getValue(page, localIndex)) != 8 || binary.LittleEndian.Uint64(getValue(page, localIndex)) != want.childPageID {
						t.Fatalf("第 %d 页第 %d 项未保留原 key/页号或顺序", pageIndex, localIndex)
					}
					wantUsedBytes += 14 + len(want.minKey)
					entryIndex++
				}
				if nodeUsedBytes(page) != wantUsedBytes || wantUsedBytes > PageSize {
					t.Fatalf("第 %d 页占用=%d, want %d <= %d", pageIndex, nodeUsedBytes(page), wantUsedBytes, PageSize)
				}
				parentEntries[pageIndex] = internalEntry{getKey(page, 0), uint64(pageIndex + 100)}
			}
			if entryIndex != len(before) {
				t.Fatal("跨页目录项数量变化")
			}

			// 仅在测试中构建上一层目录，用独立线性查找校验跨页路由，尤其是挪动的尾项。
			parent, err := internalBuild(parentEntries)
			if err != nil {
				t.Fatal(err)
			}
			queries := [][]byte{nil, {0}, {0xff}, {0xff, 0xff, 0xff}}
			for _, entry := range before {
				queries = append(queries, entry.minKey, append(bytes.Clone(entry.minKey), 0))
			}
			for _, query := range queries {
				wantID := before[0].childPageID
				for _, entry := range before {
					if bytes.Compare(entry.minKey, query) <= 0 {
						wantID = entry.childPageID
					}
				}
				pageIndex := internalChild(parent, query) - 100
				if gotID := internalChild(pages[pageIndex], query); gotID != wantID {
					t.Fatalf("query=%x child=%d, want %d", query, gotID, wantID)
				}
			}

			copies := make([][]byte, len(pages))
			for pageIndex, page := range pages {
				copies[pageIndex] = bytes.Clone(page)
			}
			for entryIndex := range testCase.entries {
				clear(testCase.entries[entryIndex].minKey)
				testCase.entries[entryIndex].childPageID = 0
			}
			for pageIndex, page := range pages {
				if !bytes.Equal(page, copies[pageIndex]) {
					t.Fatal("结果借用了输入存储")
				}
				clear(page)
				for otherIndex := pageIndex + 1; otherIndex < len(pages); otherIndex++ {
					if !bytes.Equal(pages[otherIndex], copies[otherIndex]) {
						t.Fatal("结果页之间共享存储")
					}
				}
			}
		})
	}
}

func TestInternalBuildSplitInvalidEntries(t *testing.T) {
	duplicateAtBoundary := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000)
	duplicateAtBoundary[4].minKey = bytes.Clone(duplicateAtBoundary[3].minKey)
	reversedAtBoundary := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000)
	reversedAtBoundary[4].minKey = bytes.Clone(reversedAtBoundary[2].minKey)
	lateZeroID := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000)
	lateZeroID[8].childPageID = 0
	cases := []struct {
		name    string
		entries []internalEntry
		wantErr error
	}{
		{"空目录", nil, ErrInvalidInternalEntries},
		{"零页号", []internalEntry{{[]byte("a"), 0}}, ErrInvalidInternalEntries},
		{"重复key", []internalEntry{{[]byte("a"), 1}, {[]byte("a"), 2}}, ErrInvalidInternalEntries},
		{"重复空key", []internalEntry{{nil, 1}, {[]byte{}, 2}}, ErrInvalidInternalEntries},
		{"逆序key", []internalEntry{{[]byte("b"), 1}, {[]byte("a"), 2}}, ErrInvalidInternalEntries},
		{"分组交界重复key", duplicateAtBoundary, ErrInvalidInternalEntries},
		{"分组交界逆序key", reversedAtBoundary, ErrInvalidInternalEntries},
		{"两页以后才出现零页号", lateZeroID, ErrInvalidInternalEntries},
		{"key比上限多一字节", internalEntriesWithKeyLengths(1001), ErrKeyTooLarge},
		{"key超过uint16长度", internalEntriesWithKeyLengths(65536), ErrKeyTooLarge},
		{"两页以后才出现超长key", internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1001), ErrKeyTooLarge},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := make([]internalEntry, len(testCase.entries))
			for entryIndex, entry := range testCase.entries {
				before[entryIndex] = internalEntry{bytes.Clone(entry.minKey), entry.childPageID}
			}
			pages, err := internalBuildSplit(testCase.entries)
			if !errors.Is(err, testCase.wantErr) || pages != nil {
				t.Fatalf("pages=%d err=%v, want nil and %v", len(pages), err, testCase.wantErr)
			}
			for entryIndex, entry := range testCase.entries {
				if !bytes.Equal(entry.minKey, before[entryIndex].minKey) || entry.childPageID != before[entryIndex].childPageID {
					t.Fatal("失败修改了输入")
				}
			}
		})
	}
}
