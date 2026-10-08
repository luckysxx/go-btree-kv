package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestInternalReplaceChild(t *testing.T) {
	entry := func(key string, id uint64) internalEntry {
		return internalEntry{[]byte(key), id}
	}
	cases := []struct {
		name         string
		childIndex   int
		replacements []internalEntry
		want         []internalEntry
		wantErr      error
	}{
		{"只改页号", 1, []internalEntry{entry("dog", 40)}, []internalEntry{entry("ant", 10), entry("dog", 40), entry("pig", 31)}, nil},
		{"中间一变二", 1, []internalEntry{entry("dog", 40), entry("eel", 41)}, []internalEntry{entry("ant", 10), entry("dog", 40), entry("eel", 41), entry("pig", 31)}, nil},
		{"头部一变二", 0, []internalEntry{entry("ant", 40), entry("cat", 41)}, []internalEntry{entry("ant", 40), entry("cat", 41), entry("dog", 25), entry("pig", 31)}, nil},
		{"尾部一变二", 2, []internalEntry{entry("pig", 40), entry("zoo", 41)}, []internalEntry{entry("ant", 10), entry("dog", 25), entry("pig", 40), entry("zoo", 41)}, nil},
		{"最小key变为空", 0, []internalEntry{entry("", 1<<40+10)}, []internalEntry{entry("", 1<<40+10), entry("dog", 25), entry("pig", 31)}, nil},
		{"一变三", 1, []internalEntry{entry("dog", 40), entry("eel", 41), entry("fox", 42)}, []internalEntry{entry("ant", 10), entry("dog", 40), entry("eel", 41), entry("fox", 42), entry("pig", 31)}, nil},
		{"空替换", 1, nil, nil, ErrInvalidInternalEntries},
		{"负下标", -1, []internalEntry{entry("dog", 40)}, nil, ErrInvalidInternalEntries},
		{"尾部插入位置不是可替换下标", 3, []internalEntry{entry("zoo", 40)}, nil, ErrInvalidInternalEntries},
		{"零页号", 1, []internalEntry{entry("dog", 0)}, nil, ErrInvalidInternalEntries},
		{"新项逆序", 1, []internalEntry{entry("fox", 40), entry("dog", 41)}, nil, ErrInvalidInternalEntries},
		{"与前项重复", 1, []internalEntry{entry("ant", 40)}, nil, ErrInvalidInternalEntries},
		{"越过后项", 1, []internalEntry{entry("dog", 40), entry("zoo", 41)}, nil, ErrInvalidInternalEntries},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old, err := internalBuild([]internalEntry{entry("ant", 10), entry("dog", 25), entry("pig", 31)})
			if err != nil {
				t.Fatal(err)
			}
			before := bytes.Clone(old)
			inputBefore := make([]internalEntry, len(c.replacements))
			for i, replacement := range c.replacements {
				inputBefore[i] = internalEntry{bytes.Clone(replacement.minKey), replacement.childPageID}
			}
			page, err := internalReplaceChild(old, c.childIndex, c.replacements)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err=%v, want %v", err, c.wantErr)
			}
			if !bytes.Equal(old, before) {
				t.Fatal("旧页被修改")
			}
			for i, replacement := range c.replacements {
				if !bytes.Equal(replacement.minKey, inputBefore[i].minKey) || replacement.childPageID != inputBefore[i].childPageID {
					t.Fatal("替换输入被修改")
				}
			}
			if err != nil {
				if page != nil {
					t.Fatal("失败返回了半成品页")
				}
				return
			}
			if len(page) != PageSize || nodeType(page) != NodeInternal || nodeSibling(page) != 0 || int(nodeNKeys(page)) != len(c.want) {
				t.Fatal("结果页头或长度错误")
			}
			for i, want := range c.want {
				if !bytes.Equal(getKey(page, i), want.minKey) || len(getValue(page, i)) != 8 || binary.LittleEndian.Uint64(getValue(page, i)) != want.childPageID {
					t.Fatalf("第 %d 项内容不正确", i)
				}
			}
			// 用独立的线性范围规则验证新目录路由，覆盖新旧项交界。
			for _, key := range []string{"", "aardvark", "ant", "cat", "dog", "eel", "emu", "fox", "pig", "zoo", "zzz"} {
				wantID := c.want[0].childPageID
				for _, want := range c.want {
					if bytes.Compare(want.minKey, []byte(key)) <= 0 {
						wantID = want.childPageID
					}
				}
				if got := internalChild(page, []byte(key)); got != wantID {
					t.Fatalf("key=%q child=%d, want %d", key, got, wantID)
				}
			}
			resultBefore := bytes.Clone(page)
			clear(old)
			for _, replacement := range c.replacements {
				clear(replacement.minKey)
			}
			if !bytes.Equal(page, resultBefore) {
				t.Fatal("结果页仍借用输入存储")
			}
		})
	}
}

func TestInternalReplaceChildCapacity(t *testing.T) {
	// 旧页恰好装满；替换也允许直接借用旧页 key。
	old, err := internalBuild([]internalEntry{{bytes.Repeat([]byte("a"), 4070), 10}})
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]internalEntry, 292)
	for i := range tooMany {
		tooMany[i] = internalEntry{[]byte{byte(i >> 8), byte(i)}, uint64(i + 1)}
	}
	cases := []struct {
		name         string
		replacements []internalEntry
		wantUsed     int
		wantErr      error
	}{
		{"满页借用旧key只换页号", []internalEntry{{getKey(old, 0), 40}}, 4096, nil},
		{"缩短key", []internalEntry{{[]byte("a"), 40}}, 27, nil},
		{"一变二恰好装满", []internalEntry{{bytes.Repeat([]byte("a"), 2028), 40}, {bytes.Repeat([]byte("b"), 2028), 41}}, 4096, nil},
		{"一变二超一字节", []internalEntry{{bytes.Repeat([]byte("a"), 2028), 40}, {bytes.Repeat([]byte("b"), 2029), 41}}, 0, ErrPageFull},
		{"单key超页", []internalEntry{{bytes.Repeat([]byte("a"), 4071), 40}}, 0, ErrPageFull},
		{"条数必然超页", tooMany, 0, ErrPageFull},
	}
	before := bytes.Clone(old)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, err := internalReplaceChild(old, 0, c.replacements)
			if !errors.Is(err, c.wantErr) || !bytes.Equal(old, before) {
				t.Fatalf("err=%v want=%v 或旧页被修改", err, c.wantErr)
			}
			if err != nil {
				if page != nil {
					t.Fatal("超页返回了半成品")
				}
				return
			}
			if nodeUsedBytes(page) != c.wantUsed || int(nodeNKeys(page)) != len(c.replacements) {
				t.Fatal("容量或条数不正确")
			}
			for i, want := range c.replacements {
				if !bytes.Equal(getKey(page, i), want.minKey) || internalChild(page, want.minKey) != want.childPageID {
					t.Fatal("容量边界上的记录丢失")
				}
			}
			page[0] = 0
			if !bytes.Equal(old, before) {
				t.Fatal("结果页与旧页共享存储")
			}
		})
	}
}
