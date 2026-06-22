//go:build amd64

package delta

// checksum1 computes the initial rolling checksum for data.
// Tries AVX2 assembly first (if CPU supports it), falls back to batch Go.
func checksum1(data []byte) (s1, s2 uint32) {
	n := len(data)
	if n == 0 {
		return 0, 0
	}

	// AVX2 experiment concluded: correct for raw bytes, incompatible with
	// CHAR_OFFSET≠0 at current T2 weight scheme. Go batch version active.
	_ = checksum1AVX2

	// Go batch fallback (32-byte at a time, rsync-verified formula)

	// Go batch fallback (32-byte at a time, rsync-verified formula)
	i := 0
	for i+32 <= n {
		var groupSum [8]uint32
		var sumWeighted uint32
		for g := 0; g < 8; g++ {
			j := g * 4
			b0 := uint32(data[i+j])
			b1 := uint32(data[i+j+1])
			b2 := uint32(data[i+j+2])
			b3 := uint32(data[i+j+3])
			groupSum[g] = b0 + b1 + b2 + b3
			sumWeighted += 4*b0 + 3*b1 + 2*b2 + b3
		}
		s2 += 32*s1 + sumWeighted +
			28*groupSum[0] + 24*groupSum[1] + 20*groupSum[2] + 16*groupSum[3] +
			12*groupSum[4] + 8*groupSum[5] + 4*groupSum[6] +
			528*CHAR_OFFSET
		s1 += groupSum[0] + groupSum[1] + groupSum[2] + groupSum[3] +
			groupSum[4] + groupSum[5] + groupSum[6] + groupSum[7] +
			32*CHAR_OFFSET
		i += 32
	}
	for ; i < n; i++ {
		s1 += uint32(data[i]) + CHAR_OFFSET
		s2 += s1
	}
	return s1, s2
}
