// Package util provides shared utilities used across the shuttle codebase.
package util

import (
	"fmt"

	"github.com/mattn/go-runewidth"
)

// Version is the current Shuttle version string.
// Version 当前 Shuttle 版本号。
const Version = "0.1.6.0"

// FormatBytes formats a byte count as a human-readable string using binary
// prefixes (e.g. "1.5 MiB", base-1024).
// FormatBytes 将字节数格式化为人类可读字符串（如 "1.5 MiB"，基数为 1024）。
func FormatBytes(b int64) string {
	if b < 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	// "KMGTPEZY" covers up to YiB; exp is clamped to avoid index panic.
	// int64 max ≈ 8 EiB, well within range, but the guard protects against
	// future type changes (e.g. uint64).
	const prefixes = "KMGTPEZY"
	if exp >= len(prefixes) {
		exp = len(prefixes) - 1
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), prefixes[exp])
}

// Pad pads s to width with trailing spaces (right-aligned would be different).
// Pad 将字符串右侧填充空格至指定宽度。
func Pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + spaces(width-len(s))
}

// Truncate truncates s so its terminal display width is at most width,
// appending "..." when shortened. Width is measured in display columns (CJK
// and other wide runes count as 2), not runes, so Chinese file names never
// push the progress bar past the terminal width either.
// Used to keep the progress bar within one terminal line (80 columns): an
// over-long file name widens the bar past the terminal width, the terminal
// soft-wraps, and the CR goes back to the wrapped (physical) line start, so
// every frame appears on a new line instead of refreshing in place.
// Truncate 将字符串截断到显示宽度至多 width 列，截断时追加省略号。宽度按终端
// 显示列计算（CJK/全角字符算 2 列）而非字符数，保证中文文件名也不会把进度条
// 撑过终端宽度。用于让进度条保持在一行内（80 列）：超长文件名会把进度条撑过
// 终端宽度，终端自动折行后 CR 只能回到折行后的物理行行首，导致每帧都显示在
// 新的一行而不是原地刷新。
func Truncate(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "...")
}

var spaceBuf = "                                " // 32 spaces

func spaces(n int) string {
	if n <= 32 {
		return spaceBuf[:n]
	}
	return spaceBuf + spaces(n-32)
}
