package pager

import (
	"os"
	"testing"
)

const testPath = "/tmp/pager_test.bin"

func cleanup() { _ = os.Remove(testPath) }

// 测试 1：新建数据库 → 关闭 → 重开，nPages 持久化正确
func TestPersistNPages(t *testing.T) {
	cleanup()
	defer cleanup()

	// Round 1: 新建，分 3 个新页
	p, err := Open(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if p.NPages() != 1 {
		t.Fatalf("初始 nPages 应该是 1，得到 %d", p.NPages())
	}
	for i := 0; i < 3; i++ {
		id, _, err := p.NewPage()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("分配 page id = %d", id)
	}
	if p.NPages() != 4 {
		t.Fatalf("分配 3 页后 nPages 应该是 4，得到 %d", p.NPages())
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	p.Close()

	// Round 2: 重开，nPages 应该还是 4
	p2, err := Open(testPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	if p2.NPages() != 4 {
		t.Fatalf("重开后 nPages 应该是 4，得到 %d", p2.NPages())
	}
}

// 测试 2：写入某页 → 重开后能读回
func TestPersistPageContent(t *testing.T) {
	cleanup()
	defer cleanup()

	p, err := Open(testPath)
	if err != nil {
		t.Fatal(err)
	}
	id, page, err := p.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	copy(page, []byte("hello from page!"))
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	p.Close()

	// 重开后读取
	p2, _ := Open(testPath)
	defer p2.Close()
	got := p2.GetPage(id)
	if string(got[:16]) != "hello from page!" {
		t.Fatalf("内容不对：%q", string(got[:16]))
	}
}

// 测试 3：跨 chunk 分配
// chunkSize = 16KB, pageSize = 4KB → 一个 chunk 装 4 页
// 分配 10 页应触发 3 次 grow
func TestGrowAcrossChunks(t *testing.T) {
	cleanup()
	defer cleanup()

	p, err := Open(testPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	for i := 0; i < 10; i++ {
		id, page, err := p.NewPage()
		if err != nil {
			t.Fatal(err)
		}
		page[0] = byte(id) // 在每页第一字节写它的 id 作记号
	}
	if p.NPages() != 11 { // 初始 1 + 新分配 10
		t.Fatalf("nPages 应该是 11，得到 %d", p.NPages())
	}
	// 校验每页第一字节
	for id := uint64(1); id < p.NPages(); id++ {
		got := p.GetPage(id)[0]
		if got != byte(id) {
			t.Fatalf("page %d 第一字节应该是 %d，得到 %d", id, id, got)
		}
	}
}
