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
		{"ones-128", bytesRepeat(128, 0xFF)},
		{"inc-128", incBytes(128)},
		{"rand-128", randBytes(128)},
		{"zeros-256", make([]byte, 256)},
		{"ones-700", bytesRepeat(700, 0xFF)},
		{"rand-700", randBytes(700)},
		{"rand-1024", randBytes(1024)},
		{"rand-5000", randBytes(5000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantS1, wantS2 := referenceChecksum1(tt.data)

			var avxS1, avxS2 uint32
			if !checksum1AVX2(tt.data, &avxS1, &avxS2) {
				t.Fatal("AVX2 refused")
			}
			// AVX2 returns raw checksums (CHAR_OFFSET=0). Apply CHAR_OFFSET correction:
			// s1 += p*C, s2 += C * p*(p+1)/2, plus remainder.
			p := len(tt.data) - len(tt.data)%64
			avxS1 += uint32(p) * CHAR_OFFSET
			avxS2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
			rem := len(tt.data) % 64
			for i := len(tt.data) - rem; i < len(tt.data); i++ {
				avxS1 += uint32(tt.data[i]) + CHAR_OFFSET
				avxS2 += avxS1
			}

			if avxS1 != wantS1 || avxS2 != wantS2 {
				t.Errorf("s1 want=%d got=%d, s2 want=%d got=%d",
					wantS1, avxS1, wantS2, avxS2)
			}
		})
	}
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
