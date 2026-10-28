package tracking_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

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
func TestAnotherProcessCannotWriteTheDataDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracking.sqlite")
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
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
	defer command.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatal("writer subprocess failed to start")
	}
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
