// Step 3: fsync 现身 —— 亲眼看它有多慢
//
// 目标：
//   测出你机器上单次 fsync 的延迟。这个数字决定了数据库 TPS 的上限：
//     1 秒 / fsync 延迟 = 最大每秒提交事务数（不做 batching 的话）。
//
// 运行：
//   go run ./exercises/step3_fsync_timing
//
// 典型结果（macOS / SSD）：
//   不 fsync 1000 次：~3ms     (平均 ~3µs/次)   ← 纯走 page cache
//   带 fsync 1000 次：~1-4s    (平均 ~1-4ms/次)  ← 等磁盘真正持久化
//
// macOS 注意事项：
//   macOS 的 fsync 实际上没有 Linux 严格。要真正确保数据落到非易失介质，
//   需要 fcntl(F_FULLSYNC)。所以 Mac 上测着挺快，部署到 Linux 反而变慢——
//   这是一个经典的"开发环境 vs 生产环境"陷阱。
//
// 为什么这个数字决定一切：
//   - fsync 是数据库 commit 的成本下限。
//   - 这就是为什么数据库要做 WAL（顺序追加日志）+ group commit（批量 fsync）。
//   - 你写过 raft，工业级 raft 实现也都做 batch fsync，思路完全一样。
package main

import (
	"fmt"
	"os"
	"time"
)

func writeLoop(path string, n int, withFsync bool) time.Duration {
	fp, err := os.OpenFile(path,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(err)
	}
	defer fp.Close()

	data := []byte("hello\n")
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := fp.Write(data); err != nil {
			panic(err)
		}
		if withFsync {
			if err := fp.Sync(); err != nil {
				panic(err)
			}
		}
	}
	return time.Since(start)
}

func main() {
	n := 1000
	d1 := writeLoop("/tmp/no_fsync.txt", n, false)
	d2 := writeLoop("/tmp/with_fsync.txt", n, true)

	fmt.Printf("不 fsync %d 次：%v   (平均 %v/次)\n", n, d1, d1/time.Duration(n))
	fmt.Printf("带 fsync %d 次：%v   (平均 %v/次)\n", n, d2, d2/time.Duration(n))
	fmt.Printf("慢了 %.1f 倍\n", float64(d2)/float64(d1))
}
