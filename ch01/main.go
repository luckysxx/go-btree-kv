package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"
)

// os.WriteFile 内部约等于
//   fd = open(path, O_WRONLY|O_CREATE|O_TRUNC, 0644)  // ← O_TRUNC 先把文件清空
//   write(fd, data)
//   close(fd)
//
// 问题：
//   - O_TRUNC 瞬间文件变 0 字节，此刻断电旧数据没了、新数据也没写 → 文件直接消失
//   - write 只把数据交给 page cache，没 fsync → 断电丢
//   - write 本身不是原子的 → 写一半断电 = 新旧混合
func SaveData1(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

// 用临时文件 + rename 解决原子性：rename 是原子的，要么旧 inode 要么新 inode，
// 不存在中间态。但还没 fsync → 数据可能还在 page cache，断电丢。
func SaveData2(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, rand.Int())
	fp, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer fp.Close()

	if _, err := fp.Write(data); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// 加 fsync 解决持久性。严格说还应该 fsync 父目录确保 rename 这条元数据落盘。
//
// 注意点（Go 语法陷阱）：
//   - 用命名返回值 (err error)，让 defer 闭包能看到最终的错误
//   - 内部所有错误赋值用 = 而不是 :=，否则会 shadow 外层 err
func SaveData3(path string, data []byte) (err error) {
	tmp := fmt.Sprintf("%s.tmp.%d", path, rand.Int())
	fp, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() {
		fp.Close()
		if err != nil {
			os.Remove(tmp) // 任何一步出错都清理 tmp 文件
		}
	}()

	if _, err = fp.Write(data); err != nil {
		return err
	}
	if err = fp.Sync(); err != nil {
		return err
	}
	err = os.Rename(tmp, path)
	return err
}

// 测试 SaveData2 vs SaveData3 的性能差异。
// 注意这里测的语义：每次都是完整的 open + write + (fsync?) + rename，
// 比单 fd 上的纯 write 循环更接近真实"保存配置文件"的场景。
func writeLoop(path string, n int, withFsync bool) time.Duration {
	data := []byte("hello\n")
	start := time.Now()
	for i := 0; i < n; i++ {
		var err error
		if withFsync {
			err = SaveData3(path, data)
		} else {
			err = SaveData2(path, data)
		}
		if err != nil {
			panic(err)
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
