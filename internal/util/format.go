// Package util provides shared utilities used across the shuttle codebase.
package util

import "fmt"

// Version is the current Shuttle version string.
// Version 当前 Shuttle 版本号。
const Version = "0.1.5.21"

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

var spaceBuf = "                                " // 32 spaces

func spaces(n int) string {
	if n <= 32 {
		return spaceBuf[:n]
	}
	return spaceBuf + spaces(n-32)
}
