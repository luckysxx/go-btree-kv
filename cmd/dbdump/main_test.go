package main

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunRejectsIncompleteMeta(t *testing.T) {
	for _, size := range []int{0, 1, 8, pageSize - 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "incomplete.db")
			original := bytes.Repeat([]byte{0xab}, size)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := run([]string{path}, &out, &errOut); code != 1 {
				t.Fatalf("exit = %d, stderr = %s", code, &errOut)
			}
			if out.Len() != 0 || !strings.Contains(errOut.String(), "meta 页") {
				t.Fatalf("stdout = %q, stderr = %q", &out, &errOut)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("只读检查改变文件或读取失败：%v", err)
			}
		})
	}
}

func TestRunPageSelectionAndHex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.db")
	data := make([]byte, 4*pageSize)
	writeMeta(data, 1, 2, 3, 1) // 槽位 1：txid 1，根 2，高水位 3；槽位 0 全零
	binary.LittleEndian.PutUint16(data[2*pageSize:], nodeLeaf)
	copy(data[3*pageSize:], "hello")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--page", "3", "--bytes", "5", path}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, &errOut)
	}
	for _, want := range []string{
		"Page 0 (META slot 0)", "magic 不符（0x0）",
		"Page 1 (META slot 1) [CURRENT]", "txid=1 root=2 highWater=3 checksum ok",
		"Page 3 [ABOVE HIGH WATER]", "68 65 6c 6c 6f", "|hello|",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("输出缺少 %q：%s", want, &out)
		}
	}
	if strings.Contains(out.String(), "Page 2 ") {
		t.Fatal("--page 3 不应打印 Page 2")
	}
	out.Reset()
	if code := run([]string{path}, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, &errOut)
	}
	if !strings.Contains(out.String(), "Page 2 [ROOT] [LEAF n=0]") {
		t.Errorf("输出缺少根页标签：%s", &out)
	}
}

// 较新的槽位校验和不符时，显示"不符"，CURRENT 退回另一个槽位（与 btree 打开时的选择一致）。
func TestRunChecksumMismatchFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.db")
	data := make([]byte, 5*pageSize)
	writeMeta(data, 1, 2, 3, 1)
	writeMeta(data, 0, 3, 4, 2)
	data[offMetaRoot] ^= 0xff // 撕裂/损坏槽位 0
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{path}, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, &errOut)
	}
	for _, want := range []string{"checksum 不符", "Page 1 (META slot 1) [CURRENT]", "Page 2 [ROOT]", "Page 3 [ABOVE HIGH WATER]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("输出缺少 %q：%s", want, &out)
		}
	}
	if strings.Contains(out.String(), "Page 0 (META slot 0) [CURRENT]") {
		t.Fatal("损坏的槽位不应是 CURRENT")
	}
}

// writeMeta 按 btree/db.go 的布局在槽位 slot 写 meta（含 FNV-64a 校验和）。
func writeMeta(data []byte, slot int, root, highWater, txid uint64) {
	page := data[slot*pageSize : (slot+1)*pageSize]
	binary.LittleEndian.PutUint32(page[offMetaMagic:], metaMagic)
	binary.LittleEndian.PutUint32(page[offMetaVersion:], 2)
	binary.LittleEndian.PutUint32(page[offMetaPageSize:], pageSize)
	binary.LittleEndian.PutUint64(page[offMetaRoot:], root)
	binary.LittleEndian.PutUint64(page[offMetaHighWater:], highWater)
	binary.LittleEndian.PutUint64(page[offMetaTxid:], txid)
	h := fnv.New64a()
	h.Write(page[:offMetaChecksum])
	binary.LittleEndian.PutUint64(page[offMetaChecksum:], h.Sum64())
}

// magic 不符时不解释 meta 字段，也不按高水位打标签，但仍显示各页。
func TestRunForeignMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	data := make([]byte, 3*pageSize)
	copy(data, "not a db")
	copy(data[2*pageSize:], "page two")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{path}, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, &errOut)
	}
	if !strings.Contains(out.String(), "不解释字段") || strings.Contains(out.String(), "CURRENT]") || strings.Contains(out.String(), "HIGH WATER") {
		t.Fatalf("stdout = %s", &out)
	}
	if !strings.Contains(out.String(), "没有 magic 与校验和都正确的 meta") || !strings.Contains(out.String(), "|page two") {
		t.Fatalf("未提示无有效 meta 或未显示数据页：%s", &out)
	}
}

func TestRunCLIErrorPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.db")
	if err := os.WriteFile(path, make([]byte, pageSize), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"missing-argument", nil, 2, "用法"},
		{"negative-bytes", []string{"--bytes", "-1", path}, 2, "bytes >= 0"},
		{"negative-page", []string{"--page", "-2", path}, 2, "page >= -1"},
		{"bad-number", []string{"--bytes", "x", path}, 2, "invalid value"},
		{"page-outside-file", []string{"--page", "1", path}, 1, "不存在"},
		{"missing-file", []string{path + ".missing"}, 1, "打不开文件"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(tc.args, &out, &errOut); code != tc.code || !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("exit = %d, stderr = %q", code, &errOut)
			}
		})
	}
}

func TestRunWarnsAboutTrailingPartialPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial-tail.db")
	data := make([]byte, pageSize+1)
	writeMeta(data, 0, 2, 3, 2)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{path}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "不是 4096 的倍数") {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, &out, &errOut)
	}
}
