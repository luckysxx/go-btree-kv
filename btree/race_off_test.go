//go:build !race

package btree

// raceEnabled 表示测试是否在竞争检测（go test -race）下运行。
const raceEnabled = false
