package btree

// 进程崩溃测试（J3）：子进程执行一串事务，在提交的第 N 步向自己发送 SIGKILL；
// 父进程（测试本身）在子进程死后打开同一文件，按确认协议检查内容。
//
// 能验证：进程在任意步骤死亡后，一个没有任何内存状态的新进程能正确打开与恢复。
// 不能验证：fsync 的顺序。进程被杀时页缓存仍在，删掉全部 fsync 也能通过
// （见 TestDBProcessCrashCannotSeeMissingSync）。断电需要另行模拟。
// 见 docs/notes/02-validation-observability/03-process-crash-and-io-injection.md。

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// 子进程的参数经环境变量传递。
const (
	envCrashChild  = "BTREE_CRASH_CHILD"   // 非空：本进程是子进程
	envCrashPath   = "BTREE_CRASH_PATH"    // 数据库文件
	envCrashSeed   = "BTREE_CRASH_SEED"    // 负载的随机种子
	envCrashKillAt = "BTREE_CRASH_KILL_AT" // 在第几步（从 0 数）之前自杀；-1 表示不杀
	envCrashNoSync = "BTREE_CRASH_NOSYNC"  // 非空：sync 什么也不做（演示测试的局限）

	// 父进程读取：非空时对每一步都杀一次；默认只杀在关键步骤（见 crashKillPoints）。
	// macOS 上每次 Sync 都刷新设备缓存（F_FULLFSYNC），逐步全杀要一两分钟。
	envCrashFull = "BTREE_CRASH_FULL"
)

// crashOp 是负载中的一个操作；del 为 true 时是删除，否则是 put。
type crashOp struct {
	key string
	val string
	del bool
}

// crashWorkload 由种子确定地生成一串事务。父子进程各自调用，得到相同的负载。
func crashWorkload(seed int64) [][]crashOp {
	rng := rand.New(rand.NewSource(seed))
	txs := make([][]crashOp, 25)
	for i := range txs {
		ops := make([]crashOp, 1+rng.Intn(15))
		for j := range ops {
			key := fmt.Sprintf("key-%03d", rng.Intn(120))
			if rng.Intn(4) == 0 {
				ops[j] = crashOp{key: key, del: true}
				continue
			}
			val := strings.Repeat(string(rune('a'+rng.Intn(26))), 1+rng.Intn(300))
			ops[j] = crashOp{key: key, val: val}
		}
		txs[i] = ops
	}
	return txs
}

// crashStates 返回参考模型：states[i] 是前 i 个事务全部提交后的内容，states[0] 为空。
func crashStates(txs [][]crashOp) []map[string]string {
	states := make([]map[string]string, 0, len(txs)+1)
	current := make(map[string]string)
	states = append(states, maps.Clone(current))
	for _, ops := range txs {
		for _, op := range ops {
			if op.del {
				delete(current, op.key)
			} else {
				current[op.key] = op.val
			}
		}
		states = append(states, maps.Clone(current))
	}
	return states
}

// checkCrashState 按确认协议检查崩溃重开后的内容 got。
//
// acked 是子进程确认过的事务数（最后一行 "ack k" 的 k；一个都没确认时为 0）。
// 合法的内容只有两种：
//
//	states[acked]    已确认的 k 个事务都在，正在进行的那个没生效
//	states[acked+1]  正在进行的那个也生效了（新 meta 已进页缓存，子进程没来得及确认）；
//	                 只有 acked+1 < len(states) 时才存在
//
// 返回匹配到的下标（acked 或 acked+1）；两者内容相同时返回 acked。
// 都不匹配时返回错误：已确认的事务丢了，或者出现了任何其他状态。
func checkCrashState(got map[string]string, states []map[string]string, acked int) (int, error) {
	if maps.Equal(got, states[acked]) {
		return acked, nil
	}
	next := acked + 1
	if next < len(states) && maps.Equal(got, states[next]) {
		return next, nil
	}
	return 0, fmt.Errorf("内容（%d 条）既不是第 %d 个也不是第 %d 个事务之后的状态", len(got), acked, next)
}

func TestCheckCrashState(t *testing.T) {
	states := []map[string]string{
		{},
		{"a": "1"},
		{"a": "1", "b": "2"},
		{"a": "1", "b": "2"}, // 第 3 个事务没有改变内容（例如只删了不存在的 key）
		{"b": "2"},
	}
	cases := []struct {
		name    string
		got     map[string]string
		acked   int
		want    int
		wantErr bool
	}{
		{"停在已确认版本", states[1], 1, 1, false},
		{"正在进行的事务也生效", states[2], 1, 2, false},
		{"一个都没确认，什么都没生效", states[0], 0, 0, false},
		{"一个都没确认，第一个已生效", states[1], 0, 1, false},
		{"最后一个已确认，没有下一个", states[4], 4, 4, false},
		{"两者内容相同时返回已确认的下标", states[3], 2, 2, false},
		{"已确认的事务丢了", states[1], 2, 0, true},
		{"跳过了一个事务", states[4], 2, 0, true},
		{"最后一个已确认却是旧内容", states[2], 4, 0, true},
		{"任何其他内容", map[string]string{"x": "9"}, 1, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkCrashState(tc.got, states, tc.acked)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到下标 %d", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("得到 %d, %v；期望 %d", got, err, tc.want)
			}
		})
	}
}

// TestCrashChild 只在子进程中运行：执行负载，每次提交成功后输出 "ack k"，
// 在第 killAt 步之前 SIGKILL 自己。killAt 为 -1（完整运行）时，每一步输出
// "step <序号> <data|meta|sync>"，最后输出 "done steps=<总步数>"。
// 父进程直接运行测试时它什么也不做。
func TestCrashChild(t *testing.T) {
	if os.Getenv(envCrashChild) == "" {
		t.Skip("只在进程崩溃测试的子进程中运行")
	}
	seed, _ := strconv.ParseInt(os.Getenv(envCrashSeed), 10, 64)
	killAt, _ := strconv.Atoi(os.Getenv(envCrashKillAt))

	d, err := openDB(os.Getenv(envCrashPath))
	if err != nil {
		fmt.Printf("error open: %v\n", err)
		os.Exit(2)
	}

	// 每次写页、每次同步算一步；第 killAt 步执行之前自杀。初始化（openDB 内）不计。
	step := 0
	maybeDie := func(kind string) {
		if step == killAt {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // 信号送达前不再执行任何东西
		}
		if killAt == -1 {
			fmt.Printf("step %d %s\n", step, kind)
		}
		step++
	}
	realWrite, realSync := d.writePage, d.sync
	d.writePage = func(id uint64, page []byte) error {
		kind := "data"
		if id < firstDataPage {
			kind = "meta"
		}
		maybeDie(kind)
		return realWrite(id, page)
	}
	d.sync = func() error {
		maybeDie("sync")
		if os.Getenv(envCrashNoSync) != "" {
			return nil
		}
		return realSync()
	}

	for i, ops := range crashWorkload(seed) {
		err := d.update(func(tx *tx) error {
			for _, op := range ops {
				if op.del {
					if _, err := tx.delete([]byte(op.key)); err != nil {
						return err
					}
				} else if err := tx.put([]byte(op.key), []byte(op.val)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			fmt.Printf("error tx %d: %v\n", i+1, err)
			os.Exit(2)
		}
		fmt.Printf("ack %d\n", i+1) // commit 返回之后才确认；os.Stdout 无缓冲，立即进入管道
	}
	fmt.Printf("done steps=%d\n", step)
	os.Exit(0)
}

// crashRun 是一次子进程运行的结果。
type crashRun struct {
	acked  int      // 最后确认的事务数
	steps  int      // 没被杀时的总步数；被杀时为 -1
	kinds  []string // 完整运行时每一步的类型：data、meta、sync
	killed bool     // 是否死于 SIGKILL
}

// runCrashChild 以子进程方式运行测试二进制自身，返回它的确认与结局。
func runCrashChild(t *testing.T, path string, seed int64, killAt int, noSync bool) crashRun {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		envCrashChild+"=1",
		envCrashPath+"="+path,
		envCrashSeed+"="+strconv.FormatInt(seed, 10),
		envCrashKillAt+"="+strconv.Itoa(killAt),
	)
	if noSync {
		cmd.Env = append(cmd.Env, envCrashNoSync+"=1")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	run := crashRun{steps: -1}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		run.killed = ok && status.Signaled() && status.Signal() == syscall.SIGKILL
		if !run.killed {
			t.Fatalf("子进程异常退出：%v\nstdout:\n%s\nstderr:\n%s", err, out, &stderr)
		}
	} else if err != nil {
		t.Fatalf("无法运行子进程：%v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "ack "):
			run.acked, _ = strconv.Atoi(strings.TrimPrefix(line, "ack "))
		case strings.HasPrefix(line, "step "):
			fields := strings.Fields(line)
			run.kinds = append(run.kinds, fields[2])
		case strings.HasPrefix(line, "done steps="):
			run.steps, _ = strconv.Atoi(strings.TrimPrefix(line, "done steps="))
		case strings.HasPrefix(line, "error"):
			t.Fatalf("子进程报错：%s", line)
		}
	}
	return run
}

// crashKillPoints 从完整运行的步骤类型中选出要杀的步骤。
//
// 默认只选关键位置：每次提交的第一张与最后一张数据页之前、每次 sync 之前、写 meta 之前。
// 同一提交中间的数据页之前死亡，与第一张或最后一张之前死亡属于同一类情况。
// 设置 BTREE_CRASH_FULL 时选全部步骤。
func crashKillPoints(kinds []string) []int {
	var points []int
	for i, kind := range kinds {
		previous, next := "", ""
		if i > 0 {
			previous = kinds[i-1]
		}
		if i+1 < len(kinds) {
			next = kinds[i+1]
		}
		firstData := kind == "data" && previous != "data"
		lastData := kind == "data" && next != "data"
		if os.Getenv(envCrashFull) != "" || kind != "data" || firstData || lastData {
			points = append(points, i)
		}
	}
	return points
}

// crashSweep 对每个种子先完整跑一遍得到各步骤，再在选出的每一步各杀一次，检查重开结果。
// 返回两种结局（停在确认版本 / 正在进行的事务也生效）各出现的次数。
func crashSweep(t *testing.T, seeds []int64, noSync bool) map[string]int {
	t.Helper()
	outcomes := make(map[string]int)
	for _, seed := range seeds {
		states := crashStates(crashWorkload(seed))
		full := runCrashChild(t, filepath.Join(t.TempDir(), "full.db"), seed, -1, noSync)
		if full.killed || full.steps <= 0 || full.acked != len(states)-1 || len(full.kinds) != full.steps {
			t.Fatalf("seed %d：完整运行结果异常 %+v", seed, full)
		}

		for _, killAt := range crashKillPoints(full.kinds) {
			path := filepath.Join(t.TempDir(), fmt.Sprintf("crash-%d-%d.db", seed, killAt))
			run := runCrashChild(t, path, seed, killAt, noSync)
			if !run.killed {
				t.Fatalf("seed %d killAt %d：子进程没有被杀死（%+v）", seed, killAt, run)
			}

			d, err := openDB(path)
			if err != nil {
				t.Fatalf("seed %d killAt %d：重开失败：%v", seed, killAt, err)
			}
			checkDB(t, d)
			matched, err := checkCrashState(scanAll(t, d), states, run.acked)
			if err != nil {
				t.Fatalf("seed %d killAt %d（已确认 %d 个事务）：%v", seed, killAt, run.acked, err)
			}
			if matched == run.acked {
				outcomes["acked"]++
			} else {
				outcomes["in-flight-applied"]++
			}

			// 恢复后要能继续使用：再提交一次并重开。
			dbPut(t, d, "after-crash", "ok")
			d = reopenTestDB(t, d, path)
			if v, _ := readValue(t, d, "after-crash"); v != "ok" {
				t.Fatalf("seed %d killAt %d：恢复后的提交丢失", seed, killAt)
			}
			checkDB(t, d)
			d.close()
		}
	}
	return outcomes
}

// 进程在提交的每一步被杀后，新进程打开文件，内容必须是已确认版本或正在进行的那个事务之后的版本。
func TestDBProcessCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("会启动数百个子进程")
	}
	outcomes := crashSweep(t, []int64{1}, false)
	// 覆盖检查：两种结局都要真的出现过。
	if outcomes["acked"] == 0 || outcomes["in-flight-applied"] == 0 {
		t.Fatalf("没有覆盖两种结局：%v", outcomes)
	}
	t.Logf("结局统计：%v", outcomes)
}

// 演示进程崩溃测试的局限：sync 什么也不做时，同样的检查依然全部通过——
// 进程被杀时页缓存仍在，未同步的数据照样能读到。fsync 顺序只能靠断电模拟检验。
func TestDBProcessCrashCannotSeeMissingSync(t *testing.T) {
	if testing.Short() {
		t.Skip("会启动数百个子进程")
	}
	outcomes := crashSweep(t, []int64{1}, true)
	t.Logf("没有任何 fsync，进程崩溃测试仍全部通过：%v", outcomes)
}
