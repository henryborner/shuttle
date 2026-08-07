package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henryborner/shuttle/internal/transport"
	"github.com/mattn/go-runewidth"
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
// line-clearing sequences are emitted — only the status line.
// TestConsoleHook_NonTTY 验证 isTTY=false 时不输出进度条和清行序列，只输出状态行。
func TestConsoleHook_NonTTY(t *testing.T) {
	h := &consoleHook{isTTY: false}
	out := captureStdout(t, func() {
		h.OnFileProgress("big.bin", 5, 10)
		h.OnFileDone(transport.FileEvent{RelPath: "big.bin", IsNew: true})
	})
	if strings.Contains(out, "[====") {
		t.Fatalf("progress bar leaked into piped output: %q", out)
	}
	if strings.Contains(out, "\x1b[K") {
		t.Fatalf("line-clear sequence leaked into piped output: %q", out)
	}
	if !strings.Contains(out, "NEW") {
		t.Fatalf("status line missing: %q", out)
	}
}

// TestConsoleHook_TTY verifies that with isTTY=true the progress bar is shown
// and the line is cleared on completion via ESC[K.
// TestConsoleHook_TTY 验证 isTTY=true 时显示进度条，完成后用 ESC[K 清行。
func TestConsoleHook_TTY(t *testing.T) {
	h := &consoleHook{isTTY: true}
	out := captureStdout(t, func() {
		h.OnFileProgress("big.bin", 5, 10)
		h.OnFileDone(transport.FileEvent{RelPath: "big.bin", IsNew: true})
	})
	if !strings.Contains(out, "50%") {
		t.Fatalf("progress bar (50%%) not shown on TTY: %q", out)
	}
	if !strings.Contains(out, "\x1b[K") {
		t.Fatalf("ESC[K line-clear sequence not emitted on TTY: %q", out)
	}
}

// TestConsoleHook_ProgressLineWidth verifies that a progress frame fits in 80
// columns even for a long file name. If the bar exceeds the terminal width the
// terminal soft-wraps the line and CR only returns to the wrapped physical
// line start, so every frame lands on a new line (the reported bug).
// TestConsoleHook_ProgressLineWidth 验证即使文件名很长，进度条帧也能容纳在
// 80 列内。若进度条超过终端宽度，终端自动折行，CR 只能回到折行后的物理行
// 行首，导致每帧都显示在新的一行（本次报告的 bug）。
func TestConsoleHook_ProgressLineWidth(t *testing.T) {
	h := &consoleHook{isTTY: true}
	long := "xychartDiagram-FW5EYKEG-D-G83ZtR.js" // 32 runes, > 24 pad width
	for _, pct := range []int64{50, 89, 100} {
		out := captureStdout(t, func() {
			h.OnFileProgress(long, pct, 100)
		})
		out = strings.TrimPrefix(out, "\r\x1b[K")
		w := runewidth.StringWidth(out)
		if w > 80 {
			t.Errorf("progress line width = %d columns, want <= 80 (long name %q): %q", w, long, out)
		}
	}
	// The visible base name must be truncated, not the raw 32-rune name.
	out := captureStdout(t, func() {
		h.OnFileProgress(long, 40, 100)
	})
	if strings.Contains(out, long) {
		t.Fatalf("progress line still contains untruncated long name: %q", out)
	}
	if !strings.Contains(out, "...") {
		t.Fatalf("progress line missing ellipsis for truncated name: %q", out)
	}
}

// TestConsoleHook_ProgressLineWidth_Testdata drives OnFileProgress with real
// file names from testdata/longname (long ASCII, short control, long CJK,
// extreme 60+ char) and asserts every progress frame fits in 80 columns. This
// guards against the soft-wrap regression where an over-wide bar makes CR
// return to the wrapped physical line start and every frame lands on a new
// line.
// TestConsoleHook_ProgressLineWidth_Testdata 用 testdata/longname 里的真实长
// 文件名（长 ASCII、短对照、长中文、60+ 字符极端）驱动 OnFileProgress，断言
// 每个进度条帧都容纳在 80 列内，防止超宽折行回归（折行会让 CR 只能回到折行后
// 的物理行行首，每帧都落到新的一行）。
func TestConsoleHook_ProgressLineWidth_Testdata(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "longname")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read testdata/longname: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("testdata/longname is empty")
	}
	h := &consoleHook{isTTY: true}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		for _, pct := range []int64{17, 50, 99} { // early / mid / near-complete frames
			out := captureStdout(t, func() {
				h.OnFileProgress(full, pct, 100)
			})
			out = strings.TrimPrefix(out, "\r\x1b[K")
			if w := runewidth.StringWidth(out); w > 80 {
				t.Errorf("progress line width = %d columns for %q (pct=%d), want <= 80: %q",
					w, e.Name(), pct, out)
			}
		}
	}
}
