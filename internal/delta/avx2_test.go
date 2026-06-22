package delta

import (
	"crypto/rand"
	"testing"
)

// referenceChecksum1 computes checksum byte-by-byte (original algorithm).
func referenceChecksum1(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b) + CHAR_OFFSET
		s2 += s1
	}
	return
}

func TestAVX2Parity(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"zeros-128", make([]byte, 128)},
		{"ones-64", bytesRepeat(64, 0xFF)},
		{"ones-128", bytesRepeat(128, 0xFF)},
		{"inc-64", incBytes(64)},
		{"inc-128", incBytes(128)},
		{"rand-128", randBytes(128)},
		{"rand-700", randBytes(700)},
		{"rand-2048", randBytes(2048)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawS1, rawS2 := referenceChecksum1Raw(tt.data)
			var avxS1, avxS2 uint32
			if !checksum1AVX2(tt.data, &avxS1, &avxS2) {
				t.Fatal("AVX2 refused")
			}
			// AVX2 processes full 32B chunks only; handle remainder
			p := len(tt.data) - len(tt.data)%32
			for i := p; i < len(tt.data); i++ {
				avxS1 += uint32(tt.data[i])
				avxS2 += avxS1
			}
			if avxS1 != rawS1 || avxS2 != rawS2 {
				t.Errorf("s1 want=%d got=%d, s2 want=%d got=%d", rawS1, avxS1, rawS2, avxS2)
			}
		})
	}
}

func referenceChecksum1Raw(data []byte) (s1, s2 uint32) {
	for _, b := range data {
		s1 += uint32(b)
		s2 += s1
	}
	return
}

func bytesRepeat(n int, b byte) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = b
	}
	return d
}

func incBytes(n int) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i)
	}
	return d
}

func randBytes(n int) []byte {
	d := make([]byte, n)
	rand.Read(d)
	return d
}
