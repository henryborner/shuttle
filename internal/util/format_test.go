package util

import (
	"testing"
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
