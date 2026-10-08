package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// buildLeaf 按 notes/03-btree-pages/03-node-format-and-record-addressing.md 的格式，把若干有序 KV 编码进一个叶子 page。
// 仅测试用——真正的"插入/分裂"写路径在 3.3+ 实现。
// 注意：pairs 必须按 key 升序传入（leafGet 靠这个二分）。
func buildLeaf(pairs [][2]string) []byte {
	page := make([]byte, 4096)
	binary.LittleEndian.PutUint16(page[offType:], NodeLeaf)
	binary.LittleEndian.PutUint16(page[offNKeys:], uint16(len(pairs)))
	binary.LittleEndian.PutUint64(page[offSibling:], 0) // 0 = 无右兄弟

	kvAreaAbs := headerSize + len(pairs)*2 // KV 区绝对起点
	rel := 0                               // 相对 KV 区的游标
	for i, kv := range pairs {
		k, v := []byte(kv[0]), []byte(kv[1])
		off := kvAreaAbs + rel
		binary.LittleEndian.PutUint16(page[off:], uint16(len(k)))   // klen
		binary.LittleEndian.PutUint16(page[off+2:], uint16(len(v))) // vlen
		copy(page[off+4:], k)
		copy(page[off+4+len(k):], v)
		rel += 4 + len(k) + len(v)
		binary.LittleEndian.PutUint16(page[headerSize+i*2:], uint16(rel)) // offset[i] = 终点(相对)
	}
	return page
}

func TestLeafGet(t *testing.T) {
	page := buildLeaf([][2]string{{"cat", "meow"}, {"dog", "woof"}})

	cases := []struct {
		key  string
		want string
		ok   bool
	}{
		{"cat", "meow", true}, // 第一条
		{"dog", "woof", true}, // 最后一条
		{"ant", "", false},    // 比所有 key 都小
		{"cau", "", false},    // 夹在 cat 和 dog 之间
		{"zebra", "", false},  // 比所有 key 都大
	}
	for _, c := range cases {
		got, ok := leafGet(page, []byte(c.key))
		if ok != c.ok {
			t.Errorf("leafGet(%q) found=%v, want %v", c.key, ok, c.ok)
			continue
		}
		if ok && string(got) != c.want {
			t.Errorf("leafGet(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestLeafSeek(t *testing.T) {
	// base: cat, dog, fox（升序）。直接验证 (idx, exists) 契约。
	base := buildLeaf([][2]string{{"cat", "1"}, {"dog", "2"}, {"fox", "3"}})

	cases := []struct {
		key       string
		wantIdx   int
		wantExist bool
	}{
		{"ant", 0, false}, // 比所有都小 → 落在最前
		{"cat", 0, true},  // 命中第 0 条
		{"cau", 1, false}, // 夹在 cat/dog 之间 → 落在 dog 前
		{"dog", 1, true},  // 命中中间
		{"eel", 2, false}, // 夹在 dog/fox 之间
		{"fox", 2, true},  // 命中最后一条
		{"zoo", 3, false}, // 比所有都大 → idx = nkeys
	}
	for _, c := range cases {
		idx, exists := leafSeek(base, []byte(c.key))
		if idx != c.wantIdx || exists != c.wantExist {
			t.Errorf("leafSeek(%q) = (%d,%v); want (%d,%v)",
				c.key, idx, exists, c.wantIdx, c.wantExist)
		}
	}

	// 空叶子：任何 key 都落在 idx=0、不存在
	empty := buildLeaf(nil)
	if idx, exists := leafSeek(empty, []byte("any")); idx != 0 || exists {
		t.Errorf("leafSeek(empty) = (%d,%v); want (0,false)", idx, exists)
	}
}

func TestLeafInsertEmpty(t *testing.T) {
	// 从 0 条叶子插第一条：验证 nkeys=0 边界不崩。
	empty := buildLeaf(nil)
	np, err := leafInsert(empty, []byte("cat"), []byte("meow"))
	if err != nil {
		t.Fatal(err)
	}
	if got := int(nodeNKeys(np)); got != 1 {
		t.Fatalf("nkeys = %d, want 1", got)
	}
	if got, ok := leafGet(np, []byte("cat")); !ok || string(got) != "meow" {
		t.Errorf("leafGet(cat) = %q,%v; want meow,true", got, ok)
	}
}

func TestLeafInsertOverwriteLen(t *testing.T) {
	// 覆盖时 value 变长 / 变短：整页重建必须重算后续 offset，否则尾部错位。
	base := buildLeaf([][2]string{{"cat", "meow"}, {"dog", "woof"}, {"fox", "yip"}})

	for _, c := range []struct{ name, val string }{
		{"变长", "MEOWWWWWW"}, // 4 → 9 字节
		{"变短", "m"},         // 4 → 1 字节
	} {
		t.Run(c.name, func(t *testing.T) {
			np, err := leafInsert(base, []byte("cat"), []byte(c.val))
			if err != nil {
				t.Fatal(err)
			}
			if got := int(nodeNKeys(np)); got != 3 {
				t.Errorf("nkeys = %d, want 3（覆盖不增条数）", got)
			}
			want := map[string]string{"cat": c.val, "dog": "woof", "fox": "yip"}
			for k, v := range want {
				if got, ok := leafGet(np, []byte(k)); !ok || string(got) != v {
					t.Errorf("leafGet(%q) = %q,%v; want %q（后续 KV 不能因长度变化错位）", k, got, ok, v)
				}
			}
		})
	}
}

func TestLeafInsert(t *testing.T) {
	// 从 cat/dog 出发，分别在头/中/尾插入 + 覆盖，每次都用 leafGet 验证全量内容。
	base := buildLeaf([][2]string{{"cat", "meow"}, {"dog", "woof"}})

	cases := []struct {
		name     string
		key, val string
		want     map[string]string // 插入后这张页应有的全部 KV
		nkeys    int
	}{
		{"插到头部", "ant", "x", map[string]string{"ant": "x", "cat": "meow", "dog": "woof"}, 3},
		{"插到中间", "cow", "moo", map[string]string{"cat": "meow", "cow": "moo", "dog": "woof"}, 3},
		{"插到尾部", "fox", "yip", map[string]string{"cat": "meow", "dog": "woof", "fox": "yip"}, 3},
		{"覆盖已有", "dog", "BARK", map[string]string{"cat": "meow", "dog": "BARK"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			np, err := leafInsert(base, []byte(c.key), []byte(c.val))
			if err != nil {
				t.Fatal(err)
			}

			if got := int(nodeNKeys(np)); got != c.nkeys {
				t.Errorf("nkeys = %d, want %d", got, c.nkeys)
			}
			if got := nodeSibling(np); got != nodeSibling(base) {
				t.Errorf("sibling = %d, 没继承老页的 %d", got, nodeSibling(base))
			}
			for k, want := range c.want {
				got, ok := leafGet(np, []byte(k))
				if !ok || string(got) != want {
					t.Errorf("leafGet(%q) = %q,%v; want %q,true", k, got, ok, want)
				}
			}
			// 老页没被动过（CoW）
			if _, ok := leafGet(base, []byte(c.key)); ok && c.key != "dog" {
				t.Errorf("old 被改了！leafInsert 应该是 CoW 不碰 old")
			}
		})
	}
}

func TestLeafInsertCapacity(t *testing.T) {
	// 独立按布局造旧页，避免用待测 leafInsert 准备输入。
	// insertBase 已用 4080 字节；新增 3 字节 key、7 字节 value 恰好占满。
	insertBase := [][2]string{{"cat", ""}, {"fox", strings.Repeat("f", 4050)}}
	// overwriteBase 已用 4088 字节；中间 value 从 4 增至 12 字节时占满。
	overwriteBase := [][2]string{{"cat", ""}, {"dog", "woof"}, {"fox", strings.Repeat("f", 4045)}}
	fullBase := [][2]string{{"cat", ""}, {"dog", strings.Repeat("d", 12)}, {"fox", strings.Repeat("f", 4045)}}
	cases := []struct {
		name     string
		pairs    [][2]string
		key, val string
		wantErr  bool
		wantUsed int
	}{
		{"空页恰好装满", nil, "k", strings.Repeat("v", 4077), false, 4096},
		{"空页超出一字节", nil, "k", strings.Repeat("v", 4078), true, 0},
		{"key长度超出uint16", nil, strings.Repeat("k", 65536), "", true, 0},
		{"value长度超出uint16", nil, "k", strings.Repeat("v", 65536), true, 0},
		{"头部插入恰好装满", insertBase, "ant", "1234567", false, 4096},
		{"头部插入超出一字节", insertBase, "ant", "12345678", true, 0},
		{"中间插入恰好装满", insertBase, "dog", "1234567", false, 4096},
		{"中间插入超出一字节", insertBase, "dog", "12345678", true, 0},
		{"尾部插入恰好装满", insertBase, "yak", "1234567", false, 4096},
		{"尾部插入超出一字节", insertBase, "yak", "12345678", true, 0},
		{"覆盖变长恰好装满", overwriteBase, "dog", strings.Repeat("D", 12), false, 4096},
		{"覆盖变长超出一字节", overwriteBase, "dog", strings.Repeat("D", 13), true, 0},
		{"满页等长覆盖", fullBase, "dog", strings.Repeat("D", 12), false, 4096},
		{"满页缩短value", fullBase, "dog", "", false, 4084},
		{"满页插入空key空value", fullBase, "", "", true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := buildLeaf(c.pairs)
			binary.LittleEndian.PutUint64(old[offSibling:], 7)
			before := bytes.Clone(old)
			np, err := leafInsert(old, []byte(c.key), []byte(c.val))
			if !bytes.Equal(old, before) {
				t.Fatal("leafInsert 修改了旧页")
			}
			if c.wantErr {
				if !errors.Is(err, ErrPageFull) || np != nil {
					t.Fatalf("超页结果: page nil=%v, err=%v; want nil, ErrPageFull", np == nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(np) != PageSize || nodeType(np) != NodeLeaf || nodeSibling(np) != 7 {
				t.Fatal("新页大小、类型或 sibling 不正确")
			}
			want := make(map[string]string)
			for _, pair := range c.pairs {
				want[pair[0]] = pair[1]
			}
			want[c.key] = c.val
			if int(nodeNKeys(np)) != len(want) || nodeUsedBytes(np) != c.wantUsed {
				t.Fatalf("nkeys=%d used=%d; want %d, %d", nodeNKeys(np), nodeUsedBytes(np), len(want), c.wantUsed)
			}
			for k, v := range want {
				got, ok := leafGet(np, []byte(k))
				if !ok || string(got) != v {
					t.Fatalf("key %q 未完整保存：got length=%d, want %d, exists=%v", k, len(got), len(v), ok)
				}
			}
			// 成功返回的新页独立拥有数据；改它也不能影响旧页。
			np[0] = 0
			if !bytes.Equal(old, before) {
				t.Fatal("新旧页共享存储")
			}
		})
	}
}
