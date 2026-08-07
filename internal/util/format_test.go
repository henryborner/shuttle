package util

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		b    int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"one", 1, "1 B"},
		{"negative", -1, "0 B"},
		{"negative-large", -1024, "0 B"},
		{"1023", 1023, "1023 B"},
		{"1KiB", 1024, "1.0 KiB"},
		{"1KiB-1", 1023, "1023 B"},
		{"1500", 1500, "1.5 KiB"},
		{"1MiB", 1024 * 1024, "1.0 MiB"},
		{"1MiB+512KiB", 1024*1024 + 512*1024, "1.5 MiB"},
		{"1GiB", 1024 * 1024 * 1024, "1.0 GiB"},
		{"1TiB", 1024 * 1024 * 1024 * 1024, "1.0 TiB"},
		{"2TiB", 2 * 1024 * 1024 * 1024 * 1024, "2.0 TiB"},
		{"1PiB", 1024 * 1024 * 1024 * 1024 * 1024, "1.0 PiB"},
		{"1EiB", 1024 * 1024 * 1024 * 1024 * 1024 * 1024, "1.0 EiB"},
		{"large-negative", -1000000, "0 B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatBytes(tt.b)
			if got != tt.want {
				t.Errorf("FormatBytes(%d) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}

func TestPad(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"exact", "hello", 5, "hello"},
		{"shorter", "hi", 5, "hi   "},
		{"longer", "hello world", 5, "hello world"},
		{"empty", "", 5, "     "},
		{"zero-width", "x", 0, "x"},
		{"wide-pad", "a", 40, "a" + spacesStr(39)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Pad(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("Pad(%q, %d) = %q (len=%d), want %q (len=%d)",
					tt.s, tt.width, got, len(got), tt.want, len(tt.want))
			}
		})
	}
}

func spacesStr(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = ' '
	}
	return string(s)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"short-unchanged", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"truncate", "abcdefghij", 5, "ab..."},
		{"empty", "", 5, ""},
		{"multibyte-truncate", "文件名很长很长.txt", 6, "文..."},  // CJK 2 列/字, 显示宽 5
		{"multibyte-unchanged", "中文名.txt", 10, "中文名.txt"}, // 显示宽正好 10
		{"multibyte-exact-width", "中文名.txt", 10, "中文名.txt"},
		{"multibyte-over-width", "中文名.txt", 8, "中文..."}, // 3*2+3=9>8 → 截到 8
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
			if runewidth.StringWidth(got) > tt.width {
				t.Errorf("Truncate(%q, %d) = %q has display width %d > %d", tt.s, tt.width, got, runewidth.StringWidth(got), tt.width)
			}
		})
	}
}

func TestTruncateThenPadWidth(t *testing.T) {
	// For ASCII names, Pad+Truncate always yield exactly `width` bytes/runes,
	// keeping the progress bar within one terminal line.
	// ASCII 文件名下 Pad+Truncate 结果总是恰好 width 字节/rune，进度条保持在一行内。
	for _, s := range []string{"a.js", "xychartDiagram-FW5EYKEG-D-G83ZtR.js"} {
		got := Pad(Truncate(s, 24), 24)
		if len(got) != 24 {
			t.Errorf("Pad(Truncate(%q,24),24) has %d bytes, want 24", s, len(got))
		}
	}
	// CJK bytes are wider than one column; Pad measures bytes, so it stops
	// padding once the byte count reaches 24. Truncate caps the display width
	// at 24 columns, so the field never exceeds the budget either way.
	// CJK 字符每个占 3 字节；Pad 按字节计数。Truncate 已把显示宽度限制在 24 列，
	// 因此无论 Pad 是否补空格，名字段都不会超过 24 列预算。
	got := Pad(Truncate("中文文件名很长的文件.js", 24), 24)
	if w := runewidth.StringWidth(got); w > 24 {
		t.Errorf("Pad(Truncate(CJK,24),24) display width = %d, want <= 24", w)
	}
}
