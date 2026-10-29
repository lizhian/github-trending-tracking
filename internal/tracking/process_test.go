package tracking_test

// 本文件用真正的子进程验证内核文件锁；同进程互斥锁测试不能替代这个场景。
// 父进程重新执行测试二进制中的单个辅助测试，通过管道传递 READY/stop 协调顺序。

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// TestWriterProcess 子进程专用测试入口：通过环境变量接收数据库路径，拿锁后输出 READY，等待标准输入再释放。
// 普通 go test 没有环境变量时跳过；这里 os.Exit 是为控制子进程退出，不是业务函数错误处理范例。
func TestWriterProcess(t *testing.T) {
	path := os.Getenv("GTT_WRITER_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	store, err := tracking.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stdout, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "READY")
	bufio.NewReader(os.Stdin).ReadString('\n')
	store.Close()
	os.Exit(0)
}

// TestAnotherProcessCannotWriteTheDataDirectory 启动当前测试二进制作为真正的另一进程，确认目录锁跨进程排他，子进程退出后锁可再次获取。
func TestAnotherProcessCannotWriteTheDataDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracking.sqlite")
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
	// os.Args[0] 是当前测试二进制；-test.run 只运行辅助测试，环境变量传路径，不另写一个程序。
	command := exec.Command(os.Args[0], "-test.run=^TestWriterProcess$")
	command.Env = append(os.Environ(), "GTT_WRITER_DB="+path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	// 兜底终止子进程，防止父测试提前失败后留下持锁进程；正常路径会先通过 stdin 请求它退出。
	defer command.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatal("writer subprocess failed to start")
	}
	// 子进程确认 READY 后再尝试第二次打开，确保确实在竞争已持有的锁，而非碰巧先后执行。
	if store, err := tracking.Open(path); err == nil {
		store.Close()
		t.Fatal("two writer processes opened one directory")
	}
	fmt.Fprintln(stdin, "stop")
	if err = command.Wait(); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(path)
	if err != nil {
		t.Fatal("lock was not released", err)
	}
	store.Close()
}
