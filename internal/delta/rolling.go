package delta

const (
	CHAR_OFFSET = 31
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
	rs.s1, rs.s2 = checksum1(data)
}

// Roll 滚动窗口：移除一个旧字节，加入一个新字节，更新 s1/s2。
// 使用 uint32 自然溢出（同 rsync），无取模、无类型转换。
func (rs *RollingSum) Roll(oldByte, newByte byte, blockLen int32) {
	old := uint32(oldByte) + CHAR_OFFSET
	new := uint32(newByte) + CHAR_OFFSET

	// 同 rsync checksum.c：纯 uint32 运算，溢出自然取模
	rs.s1 += new - old
	rs.s2 += rs.s1 - uint32(blockLen)*old
}

func (rs *RollingSum) Value() uint32 {
	// 仅输出时截取低 16 位，组成 32-bit 校验和
	return (rs.s1 & 0xFFFF) | ((rs.s2 & 0xFFFF) << 16)
}

// S1 返回 s1 低 16 位
func (rs *RollingSum) S1() uint32 { return rs.s1 & 0xFFFF }

// S2 返回 s2 低 16 位
func (rs *RollingSum) S2() uint32 { return rs.s2 & 0xFFFF }

// Checksum1 直接计算一次性的滚动校验和（非滚动模式）
func Checksum1(data []byte) uint32 {
	return NewRollingSum(data).Value()
}
