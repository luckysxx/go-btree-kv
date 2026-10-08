package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLeafSplitPoint(t *testing.T) {
	cases := []struct {
		name   string
		prefix []int
		wantM  int
		wantOK bool
	}{
		{"无输入", nil, 0, false},
		{"空序列", []int{0}, 0, false},
		{"只有一条", []int{0, 1000}, 0, false},
		{"字节恰好均衡", []int{0, 1500, 3000, 4500, 6000}, 2, true},
		{"按条数均分会使左页超出", []int{0, 2500, 4100, 4300, 4500}, 1, true},
		{"按条数均分会使右页超出", []int{0, 200, 400, 2000, 4500}, 3, true},
		{"目标之前比之后更近", []int{0, 2200, 3300, 5000}, 1, true},
		{"同样均衡时选较小切点", []int{0, 2000, 4000, 6000}, 1, true},
		{"两页都恰好装满", []int{0, 4084, 8168}, 1, true},
		{"不能漏计每页页头", []int{0, 4090, 8180}, 0, false},
		{"总量只超两页一字节", []int{0, 4084, 8169}, 0, false},
		// 每条可独立放入一页，总量小于两页容量，但任意连续切点都失败。
		{"中间大记录导致两页无解", []int{0, 1000, 5000, 6000}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := slices.Clone(c.prefix)
			m, ok := leafSplitPoint(c.prefix)
			if m != c.wantM || ok != c.wantOK {
				t.Fatalf("leafSplitPoint(%v)=(%d,%v), want (%d,%v)", c.prefix, m, ok, c.wantM, c.wantOK)
			}
			if !slices.Equal(c.prefix, before) {
				t.Fatal("累计长度输入被修改")
			}
			if !ok {
				return
			}
			// 把选出的两组实际编码为页，验证容量、局部 offset 和完整数据。
			// 此处仅为测试编码，不是自动分裂的生产调用路径。
			for _, span := range [][2]int{{0, m}, {m, len(c.prefix) - 1}} {
				lo, hi := span[0], span[1]
				page := make([]byte, PageSize)
				binary.LittleEndian.PutUint16(page[offType:], NodeLeaf)
				binary.LittleEndian.PutUint16(page[offNKeys:], uint16(hi-lo))
				for i := lo; i < hi; i++ {
					// 每条用 1 字节 key，余下扣除 offset(2)+长度字段(4)。
					valLen := c.prefix[i+1] - c.prefix[i] - 7
					leafAppendKV(page, i-lo, []byte{byte('a' + i)}, bytes.Repeat([]byte{byte(i + 1)}, valLen))
				}
				if used := nodeUsedBytes(page); used != headerSize+c.prefix[hi]-c.prefix[lo] || used > PageSize {
					t.Fatalf("结果页占用不正确: used=%d", used)
				}
				for i := lo; i < hi; i++ {
					got, found := leafGet(page, []byte{byte('a' + i)})
					want := bytes.Repeat([]byte{byte(i + 1)}, c.prefix[i+1]-c.prefix[i]-7)
					if !found || !bytes.Equal(got, want) {
						t.Fatalf("记录 %d 在结果页中无法完整读取", i)
					}
				}
			}
		})
	}
}

func TestLeafInsertSplit(t *testing.T) {
	// 每条记录占用 = 2 字节表项 + 4 字节长度 + 1 字节 key + value。
	base := [][2]string{{"a", strings.Repeat("a", 2493)}, {"c", strings.Repeat("c", 193)}, {"d", strings.Repeat("d", 193)}}
	overwriteBase := [][2]string{{"a", strings.Repeat("a", 2493)}, {"b", "old"}, {"c", strings.Repeat("c", 193)}, {"d", strings.Repeat("d", 193)}}
	noCutBase := [][2]string{{"a", strings.Repeat("a", 993)}, {"c", strings.Repeat("c", 993)}}
	full := [][2]string{{"a", strings.Repeat("a", 4077)}}
	cases := []struct {
		name     string
		pairs    [][2]string
		key, val string
		counts   []int // 各结果页记录数，nil 表示失败。
		wantErr  error
	}{
		{"空页插入", nil, "a", "value", []int{1}, nil},
		{"单页插入", [][2]string{{"a", "1"}, {"c", "3"}}, "b", "2", []int{3}, nil},
		{"单页覆盖", [][2]string{{"a", "1"}, {"b", "2"}}, "b", "new", []int{2}, nil},
		{"单页恰好装满", nil, "a", strings.Repeat("a", 4077), []int{1}, nil},
		{"满页等长覆盖", full, "a", strings.Repeat("A", 4077), []int{1}, nil},
		{"满页缩短不分裂", full, "a", "", []int{1}, nil},
		{"头部插入分裂", base, "0", strings.Repeat("v", 1593), []int{1, 3}, nil},
		{"中间插入分裂", base, "b", strings.Repeat("v", 1593), []int{1, 3}, nil},
		{"尾部插入分裂", base, "z", strings.Repeat("v", 1593), []int{1, 3}, nil},
		{"覆盖变长触发分裂", overwriteBase, "b", strings.Repeat("v", 1593), []int{1, 3}, nil},
		{"两页各自恰好装满", full, "b", strings.Repeat("b", 4077), []int{1, 1}, nil},
		{"单条超出空页一字节", nil, "a", strings.Repeat("v", 4078), nil, ErrRecordTooLarge},
		{"超长key", base, strings.Repeat("k", 65536), "", nil, ErrRecordTooLarge},
		{"超长value", base, "b", strings.Repeat("v", 65536), nil, ErrRecordTooLarge},
		{"插入后两页无解则返回三页", noCutBase, "b", strings.Repeat("b", 3993), []int{1, 1, 1}, nil},
		{"覆盖后两页无解则返回三页", [][2]string{{"a", strings.Repeat("a", 993)}, {"b", "old"}, {"c", strings.Repeat("c", 993)}}, "b", strings.Repeat("b", 3993), []int{1, 1, 1}, nil},
		{"头部大记录插入只需两页", noCutBase, "0", strings.Repeat("v", 3993), []int{1, 2}, nil},
		{"尾部大记录插入只需两页", noCutBase, "z", strings.Repeat("v", 3993), []int{2, 1}, nil},
		{"头部大记录覆盖只需两页", noCutBase, "a", strings.Repeat("v", 3993), []int{1, 1}, nil},
		{"尾部大记录覆盖只需两页", noCutBase, "c", strings.Repeat("v", 3993), []int{1, 1}, nil},
		{"三页中间页恰好装满", noCutBase, "b", strings.Repeat("v", 4077), []int{1, 1, 1}, nil},
		{"中间记录与邻项恰好一页", noCutBase, "b", strings.Repeat("v", 3077), []int{1, 2}, nil},
		{"中间记录与邻项超一字节", noCutBase, "b", strings.Repeat("v", 3078), []int{1, 1, 1}, nil},
		{"三页插入保留多条前后缀", [][2]string{{"a", strings.Repeat("a", 493)}, {"b", strings.Repeat("b", 493)}, {"d", strings.Repeat("d", 493)}, {"e", strings.Repeat("e", 493)}}, "c", strings.Repeat("v", 3993), []int{2, 1, 2}, nil},
		{"三页覆盖跳过旧值并保留多条前后缀", [][2]string{{"a", strings.Repeat("a", 493)}, {"b", strings.Repeat("b", 493)}, {"c", "old"}, {"d", strings.Repeat("d", 493)}, {"e", strings.Repeat("e", 493)}}, "c", strings.Repeat("v", 3993), []int{2, 1, 2}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := buildLeaf(c.pairs)
			binary.LittleEndian.PutUint64(old[offSibling:], 77)
			before := bytes.Clone(old)
			key, val := []byte(c.key), []byte(c.val)
			pages, err := leafInsertSplit(old, key, val)
			if !bytes.Equal(old, before) || string(key) != c.key || string(val) != c.val {
				t.Fatal("插入修改了输入")
			}
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) || pages != nil {
					t.Fatalf("got %d pages, %v; want nil, %v", len(pages), err, c.wantErr)
				}
				return
			}
			if err != nil || len(pages) != len(c.counts) {
				t.Fatalf("got %d pages, %v; want %d pages, nil", len(pages), err, len(c.counts))
			}
			want := make(map[string]string)
			for _, pair := range c.pairs {
				want[pair[0]] = pair[1]
			}
			want[c.key] = c.val
			wantKeys := make([]string, 0, len(want))
			for k := range want {
				wantKeys = append(wantKeys, k)
			}
			slices.Sort(wantKeys)
			var gotKeys []string
			for pi, page := range pages {
				if len(page) != PageSize || nodeType(page) != NodeLeaf || int(nodeNKeys(page)) != c.counts[pi] || nodeUsedBytes(page) > PageSize {
					t.Fatalf("第 %d 页大小、类型、条数或容量不正确", pi)
				}
				wantSibling := uint64(0) // 非最右页等待分配相邻右页号后连接。
				if pi == len(pages)-1 {
					wantSibling = 77
				}
				if nodeSibling(page) != wantSibling {
					t.Fatalf("第 %d 页 sibling=%d, want %d", pi, nodeSibling(page), wantSibling)
				}
				for i := 0; i < int(nodeNKeys(page)); i++ {
					k := string(getKey(page, i))
					gotKeys = append(gotKeys, k)
					got, found := leafGet(page, []byte(k))
					v, exists := want[k]
					if !exists || !found || string(got) != v {
						t.Fatalf("第 %d 页记录 %q 内容不正确", pi, k)
					}
				}
			}
			// 排序后的参考 map 验证无丢失、无重复、无旧值残留，跨页也有序。
			if !slices.Equal(gotKeys, wantKeys) {
				t.Fatalf("跨页 key 序列=%v, want %v", gotKeys, wantKeys)
			}
			copies := make([][]byte, len(pages))
			for i := range pages {
				copies[i] = bytes.Clone(pages[i])
			}
			// 返回页独立拥有数据，即使调用方复用 old、key 和 value 也不改变结果。
			clear(old)
			clear(key)
			clear(val)
			for i := range pages {
				if !bytes.Equal(pages[i], copies[i]) {
					t.Fatal("结果页借用了输入存储")
				}
			}
			for pageIndex := range pages {
				clear(pages[pageIndex])
				for remainingIndex := pageIndex + 1; remainingIndex < len(pages); remainingIndex++ {
					if !bytes.Equal(pages[remainingIndex], copies[remainingIndex]) {
						t.Fatal("结果页之间共享了存储")
					}
				}
			}
		})
	}
}

func TestLeafInsertSplitBorrowedInput(t *testing.T) {
	// 插入 value 直接借用旧页中的值，也应该在重建时复制，且不破坏源页。
	old := buildLeaf([][2]string{{"a", strings.Repeat("v", 2500)}, {"z", "tail"}})
	before := bytes.Clone(old)
	pages, err := leafInsertSplit(old, []byte("b"), getValue(old, 0))
	if err != nil || len(pages) != 2 || !bytes.Equal(old, before) {
		t.Fatalf("borrowed input: pages=%d err=%v old unchanged=%v", len(pages), err, bytes.Equal(old, before))
	}
	clear(old)
	for pi, k := range []string{"a", "b"} {
		got, found := leafGet(pages[pi], []byte(k))
		if !found || string(got) != strings.Repeat("v", 2500) {
			t.Fatalf("旧页清除后结果 %q 被改变", k)
		}
	}
}
