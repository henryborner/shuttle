

package delta

const (

	CHAR_OFFSET = 31
	// MOD 模数
	MOD = 1 << 16
)


type RollingSum struct {
	count int32 // 窗口中的字节
	s1    uint32
	s2    uint32
}


func NewRollingSum(data []byte) *RollingSum {
	rs := &RollingSum{}
	rs.Reset(data)
	return rs
}


func (rs *RollingSum) Reset(data []byte) {
	rs.count = int32(len(data))
	var s1, s2 uint32

	i := 0
	n := len(data) - 4
	for ; i <= n; i += 4 {
		s2 += 4*(s1+uint32(data[i])+CHAR_OFFSET) +
			3*uint32(data[i+1]) +
			2*uint32(data[i+2]) +
			uint32(data[i+3]) +
			10*CHAR_OFFSET
		s1 += uint32(data[i+0]) + uint32(data[i+1]) +
			uint32(data[i+2]) + uint32(data[i+3]) +
			4*CHAR_OFFSET
	}
	for ; i < len(data); i++ {
		s1 += uint32(data[i]) + CHAR_OFFSET
		s2 += s1
	}
	rs.s1 = s1 & 0xFFFF
	rs.s2 = s2 & 0xFFFF
}



func (rs *RollingSum) Roll(oldByte, newByte byte, blockLen int32) {
	old := uint32(oldByte) + CHAR_OFFSET
	new := uint32(newByte) + CHAR_OFFSET

	rs.s1 = (rs.s1 - old + new) % MOD
	rs.s2 = (rs.s2 - uint32(blockLen)*old + rs.s1) % MOD
}


func (rs *RollingSum) Value() uint32 {
	return rs.s1 + (rs.s2 << 16)
}

// S1 返回 s1 分量
func (rs *RollingSum) S1() uint32 { return rs.s1 }

// S2 返回 s2 分量
func (rs *RollingSum) S2() uint32 { return rs.s2 }

// Checksum1 直接计算一次性的滚动校验和（非滚动模式）
func Checksum1(data []byte) uint32 {
	return NewRollingSum(data).Value()
}
