package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func cloneInternalEntries(entries []internalEntry) []internalEntry {
	cloned := make([]internalEntry, len(entries))
	for entryIndex, entry := range entries {
		cloned[entryIndex] = internalEntry{bytes.Clone(entry.minKey), entry.childPageID}
	}
	return cloned
}

func TestInternalReplaceChildSplit(t *testing.T) {
	entry := func(key string, pageID uint64) internalEntry {
		return internalEntry{[]byte(key), pageID}
	}
	base := []internalEntry{entry("ant", 10), entry("dog", 25), entry("pig", 31)}
	longEntries := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000)
	exactEntries := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 14)
	grownEntries := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 15)
	shortKeyLengths := make([]int, 292)
	for entryIndex := range shortKeyLengths {
		shortKeyLengths[entryIndex] = 2
	}
	manyEntries := internalEntriesWithKeyLengths(shortKeyLengths...)
	cases := []struct {
		name         string
		oldEntries   []internalEntry
		childIndex   int
		replacements []internalEntry
		borrowOldKey bool
		want         []internalEntry
		wantCounts   []int
	}{
		{
			name: "唯一孩子只换页号", oldEntries: base[:1], childIndex: 0,
			replacements: []internalEntry{entry("ant", 40)},
			want:         []internalEntry{entry("ant", 40)}, wantCounts: []int{1},
		},
		{
			name: "中间只换页号并借用旧key", oldEntries: base, childIndex: 1, borrowOldKey: true,
			replacements: []internalEntry{entry("dog", 40)},
			want:         []internalEntry{entry("ant", 10), entry("dog", 40), entry("pig", 31)}, wantCounts: []int{3},
		},
		{
			name: "中间一变二仍为单页", oldEntries: base, childIndex: 1,
			replacements: []internalEntry{entry("dog", 40), entry("eel", 41)},
			want:         []internalEntry{entry("ant", 10), entry("dog", 40), entry("eel", 41), entry("pig", 31)}, wantCounts: []int{4},
		},
		{
			name: "头部一变二并改变最小key", oldEntries: base, childIndex: 0,
			replacements: []internalEntry{entry("", 40), entry("cat", 41)},
			want:         []internalEntry{entry("", 40), entry("cat", 41), entry("dog", 25), entry("pig", 31)}, wantCounts: []int{4},
		},
		{
			name: "尾部一变二包含二进制key", oldEntries: base, childIndex: 2,
			replacements: []internalEntry{entry("pig", 40), entry("\xff\x00", 41)},
			want:         []internalEntry{entry("ant", 10), entry("dog", 25), entry("pig", 40), entry("\xff\x00", 41)}, wantCounts: []int{4},
		},
		{
			name: "中间一变三仍为单页", oldEntries: base, childIndex: 1,
			replacements: []internalEntry{entry("dog", 40), entry("eel", 41), entry("fox", 42)},
			want:         []internalEntry{entry("ant", 10), entry("dog", 40), entry("eel", 41), entry("fox", 42), entry("pig", 31)}, wantCounts: []int{5},
		},
		{
			name:       "中间一变二分裂并调整尾组",
			oldEntries: []internalEntry{longEntries[0], {longEntries[1].minKey, 25}, longEntries[3], longEntries[4]}, childIndex: 1,
			replacements: longEntries[1:3], want: longEntries[:5], wantCounts: []int{3, 2},
		},
		{
			name:       "中间一变三分裂为四加二",
			oldEntries: []internalEntry{longEntries[0], {longEntries[1].minKey, 25}, longEntries[4], longEntries[5]}, childIndex: 1,
			replacements: longEntries[1:4], want: longEntries[:6], wantCounts: []int{4, 2},
		},
		{
			name:       "尾部替换后恰好装满一页",
			oldEntries: []internalEntry{exactEntries[0], exactEntries[1], exactEntries[2], {exactEntries[3].minKey, 25}}, childIndex: 3,
			replacements: exactEntries[3:], want: exactEntries, wantCounts: []int{5},
		},
		{
			name: "只增长一字节也触发分裂", oldEntries: exactEntries, childIndex: 4,
			replacements: grownEntries[4:], want: grownEntries, wantCounts: []int{3, 2},
		},
		{
			name: "一般替换列表可产生三页", oldEntries: base[:1], childIndex: 0,
			replacements: longEntries, want: longEntries, wantCounts: []int{4, 3, 2},
		},
		{
			name: "条数超过单页上限仍成功", oldEntries: base[:1], childIndex: 0,
			replacements: manyEntries, want: manyEntries, wantCounts: []int{255, 37},
		},
		{
			name:       "被移除的超长旧key不进入校验",
			oldEntries: []internalEntry{{bytes.Repeat([]byte("a"), 1001), 10}}, childIndex: 0,
			replacements: []internalEntry{entry("a", 40)}, want: []internalEntry{entry("a", 40)}, wantCounts: []int{1},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			old, err := internalBuild(testCase.oldEntries)
			if err != nil {
				t.Fatal(err)
			}
			replacements := cloneInternalEntries(testCase.replacements)
			if testCase.borrowOldKey {
				replacements[0].minKey = getKey(old, testCase.childIndex)
			}
			oldBefore := bytes.Clone(old)
			replacementsBefore := cloneInternalEntries(replacements)
			pages, err := internalReplaceChildSplit(old, testCase.childIndex, replacements)
			if err != nil || len(pages) != len(testCase.wantCounts) {
				t.Fatalf("pages=%d err=%v, want %d pages", len(pages), err, len(testCase.wantCounts))
			}
			if !bytes.Equal(old, oldBefore) {
				t.Fatal("替换修改了旧页")
			}
			for entryIndex, replacement := range replacements {
				before := replacementsBefore[entryIndex]
				if !bytes.Equal(replacement.minKey, before.minKey) || replacement.childPageID != before.childPageID {
					t.Fatal("替换修改了新目录输入")
				}
			}

			entryIndex := 0
			parentEntries := make([]internalEntry, len(pages))
			for pageIndex, page := range pages {
				if len(page) != PageSize || nodeType(page) != NodeInternal || nodeSibling(page) != 0 || int(nodeNKeys(page)) != testCase.wantCounts[pageIndex] {
					t.Fatalf("第 %d 页长度、页头或条数错误", pageIndex)
				}
				wantUsedBytes := headerSize
				for localIndex := 0; localIndex < int(nodeNKeys(page)); localIndex++ {
					if entryIndex >= len(testCase.want) {
						t.Fatal("结果含多余目录项")
					}
					want := testCase.want[entryIndex]
					if !bytes.Equal(getKey(page, localIndex), want.minKey) || len(getValue(page, localIndex)) != 8 || binary.LittleEndian.Uint64(getValue(page, localIndex)) != want.childPageID {
						t.Fatalf("第 %d 页第 %d 项 key、页号或顺序错误", pageIndex, localIndex)
					}
					wantUsedBytes += 14 + len(want.minKey)
					entryIndex++
				}
				if nodeUsedBytes(page) != wantUsedBytes || wantUsedBytes > PageSize {
					t.Fatal("结果页容量错误")
				}
				parentEntries[pageIndex] = internalEntry{getKey(page, 0), uint64(pageIndex + 100)}
			}
			if entryIndex != len(testCase.want) {
				t.Fatal("替换后丢失目录项")
			}

			// 测试中临时连接上一层，与独立线性查找比较跨新旧项、跨结果页的路由。
			parent, err := internalBuild(parentEntries)
			if err != nil {
				t.Fatal(err)
			}
			queries := [][]byte{nil, {0}, {0xff, 0xff, 0xff}}
			for _, want := range testCase.want {
				queries = append(queries, want.minKey, append(bytes.Clone(want.minKey), 0))
			}
			for _, query := range queries {
				wantID := testCase.want[0].childPageID
				for _, want := range testCase.want {
					if bytes.Compare(want.minKey, query) <= 0 {
						wantID = want.childPageID
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
			clear(old)
			for entryIndex := range replacements {
				clear(replacements[entryIndex].minKey)
				replacements[entryIndex].childPageID = 0
			}
			for pageIndex, page := range pages {
				if !bytes.Equal(page, copies[pageIndex]) {
					t.Fatal("结果仍借用旧页或替换输入")
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

func TestInternalReplaceChildSplitInvalidEntries(t *testing.T) {
	entry := func(key string, pageID uint64) internalEntry {
		return internalEntry{[]byte(key), pageID}
	}
	base := []internalEntry{entry("ant", 10), entry("dog", 25), entry("pig", 31)}
	longKey := bytes.Repeat([]byte("a"), 1001)
	lateZeroID := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000)
	lateZeroID[8].childPageID = 0
	lateLongKey := internalEntriesWithKeyLengths(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1001)
	cases := []struct {
		name         string
		oldEntries   []internalEntry
		childIndex   int
		replacements []internalEntry
		wantErr      error
	}{
		{"空替换", base, 1, nil, ErrInvalidInternalEntries},
		{"负下标", base, -1, []internalEntry{entry("dog", 40)}, ErrInvalidInternalEntries},
		{"下标等于旧项数", base, 3, []internalEntry{entry("zoo", 40)}, ErrInvalidInternalEntries},
		{"零页号", base, 1, []internalEntry{entry("dog", 0)}, ErrInvalidInternalEntries},
		{"新项重复", base, 1, []internalEntry{entry("dog", 40), entry("dog", 41)}, ErrInvalidInternalEntries},
		{"新项逆序", base, 1, []internalEntry{entry("fox", 40), entry("dog", 41)}, ErrInvalidInternalEntries},
		{"与旧前项重复", base, 1, []internalEntry{entry("ant", 40)}, ErrInvalidInternalEntries},
		{"越过旧前项", base, 1, []internalEntry{entry("a", 40)}, ErrInvalidInternalEntries},
		{"与旧后项重复", base, 1, []internalEntry{entry("dog", 40), entry("pig", 41)}, ErrInvalidInternalEntries},
		{"越过旧后项", base, 1, []internalEntry{entry("dog", 40), entry("zoo", 41)}, ErrInvalidInternalEntries},
		{"能装单页的新key仍检查上限", base[:1], 0, []internalEntry{{longKey, 40}}, ErrKeyTooLarge},
		{"保留的旧前项也检查上限", []internalEntry{{longKey, 10}, entry("b", 25)}, 1, []internalEntry{entry("b", 40)}, ErrKeyTooLarge},
		{"保留的旧后项也检查上限", []internalEntry{entry("", 10), {longKey, 25}}, 0, []internalEntry{entry("", 40)}, ErrKeyTooLarge},
		{"足够形成两页后遇到零页号", base[:1], 0, lateZeroID, ErrInvalidInternalEntries},
		{"足够形成两页后遇到超长key", base[:1], 0, lateLongKey, ErrKeyTooLarge},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			old, err := internalBuild(testCase.oldEntries)
			if err != nil {
				t.Fatal(err)
			}
			oldBefore := bytes.Clone(old)
			replacementsBefore := cloneInternalEntries(testCase.replacements)
			pages, err := internalReplaceChildSplit(old, testCase.childIndex, testCase.replacements)
			if !errors.Is(err, testCase.wantErr) || pages != nil {
				t.Fatalf("pages=%d err=%v, want nil and %v", len(pages), err, testCase.wantErr)
			}
			if !bytes.Equal(old, oldBefore) {
				t.Fatal("失败修改了旧页")
			}
			for entryIndex, replacement := range testCase.replacements {
				before := replacementsBefore[entryIndex]
				if !bytes.Equal(replacement.minKey, before.minKey) || replacement.childPageID != before.childPageID {
					t.Fatal("失败修改了替换输入")
				}
			}
		})
	}
}
