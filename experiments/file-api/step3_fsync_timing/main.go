// Step 3: 比较循环 write 与 write + Sync 的本机耗时。
//
// 运行：go run ./experiments/file-api/step3_fsync_timing -n 1000
// 快速检查可用 -n 5；省略参数仍执行 1000 次。
//
// 平均耗时只描述本机、本次运行和这一串行负载；不直接等于通用 TPS 上限。
// 结果受 OS、文件系统、设备和缓存影响，不能从快慢推断断电保证。
// 实验未注入断电，也没有完成文件/目录的完整持久化提交协议。
// 批量提交对同步次数的影响留给后续学习。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"dbstudy/experiments/internal/demofs"
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
	count := flag.Int("n", 1000, "每种模式执行的次数，必须大于 0")
	flag.Parse()
	if *count <= 0 {
		fmt.Fprintln(os.Stderr, "-n 必须大于 0")
		os.Exit(2)
	}
	n := *count
	dir := demofs.NewDir("sync-timing")
	d1 := writeLoop(filepath.Join(dir, "no_fsync.txt"), n, false)
	d2 := writeLoop(filepath.Join(dir, "with_fsync.txt"), n, true)

	fmt.Printf("不 fsync %d 次：%v   (平均 %v/次)\n", n, d1, d1/time.Duration(n))
	fmt.Printf("带 fsync %d 次：%v   (平均 %v/次)\n", n, d2, d2/time.Duration(n))
	fmt.Printf("耗时比（带 Sync / 不带 Sync）：%.1f\n", float64(d2)/float64(d1))
	fmt.Println("这是本机本次调用的耗时对比，不验证断电恢复，也不是通用 TPS 上限。")
}
