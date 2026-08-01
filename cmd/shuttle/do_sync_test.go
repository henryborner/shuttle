package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/henryborner/shuttle/internal/transport"
)

// captureStdout runs fn with os.Stdout replaced by a pipe and returns what
// was written to it.
// captureStdout 用管道替换 os.Stdout 执行 fn，返回写入的内容。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

// TestStdoutIsTTYFalse verifies that under `go test` (piped stdout) the TTY
// detector reports false, so the console hook stays quiet.
// TestStdoutIsTTYFalse 验证 go test 环境（stdout 为管道）下 TTY 检测为 false。
func TestStdoutIsTTYFalse(t *testing.T) {
	if stdoutIsTTY() {
		t.Fatal("stdoutIsTTY() = true under piped test output, want false")
	}
}

// TestConsoleHook_NonTTY verifies that with isTTY=false no progress bar and no
// line-clearing spaces are emitted — only the status line.
// TestConsoleHook_NonTTY 验证 isTTY=false 时不输出进度条和清行空格，只输出状态行。
func TestConsoleHook_NonTTY(t *testing.T) {
	h := &consoleHook{isTTY: false}
	out := captureStdout(t, func() {
		h.OnFileProgress("big.bin", 5, 10)
		h.OnFileDone(transport.FileEvent{RelPath: "big.bin", IsNew: true})
	})
	if strings.Contains(out, "[====") {
		t.Fatalf("progress bar leaked into piped output: %q", out)
	}
	if strings.Contains(out, strings.Repeat(" ", 80)) {
		t.Fatalf("line-clear spaces leaked into piped output: %q", out)
	}
	if !strings.Contains(out, "NEW") {
		t.Fatalf("status line missing: %q", out)
	}
}

// TestConsoleHook_TTY verifies that with isTTY=true the progress bar is shown
// and the line is cleared on completion.
// TestConsoleHook_TTY 验证 isTTY=true 时显示进度条，完成后清行。
func TestConsoleHook_TTY(t *testing.T) {
	h := &consoleHook{isTTY: true}
	out := captureStdout(t, func() {
		h.OnFileProgress("big.bin", 5, 10)
		h.OnFileDone(transport.FileEvent{RelPath: "big.bin", IsNew: true})
	})
	if !strings.Contains(out, "50%") {
		t.Fatalf("progress bar (50%%) not shown on TTY: %q", out)
	}
	if !strings.Contains(out, strings.Repeat(" ", 80)) {
		t.Fatalf("line-clear sequence not emitted on TTY: %q", out)
	}
}
