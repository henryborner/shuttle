// Package util provides shared utilities used across the shuttle codebase.
package util

import "fmt"

// FormatBytes formats a byte count as a human-readable string (e.g. "1.5 MB").
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Pad pads s to width with spaces on the right.
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
