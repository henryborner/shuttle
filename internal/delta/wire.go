

package delta

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WireEncodeSignature 将签名编码为二进制流
func WireEncodeSignature(w io.Writer, sig *Signature) error {
	// 头部: blockSize(4) + fileSize(8) + count(4)
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header[0:4], uint32(sig.BlockSize))
	binary.BigEndian.PutUint64(header[4:12], uint64(sig.FileSize))
	binary.BigEndian.PutUint32(header[12:16], uint32(len(sig.BlockSums)))
	if _, err := w.Write(header); err != nil {
		return err
	}

	for _, bs := range sig.BlockSums {
		// index(4) + sum1(4) + sum2Len(1) + sum2(N) + offset(8) + length(4)
		buf := make([]byte, 4+4+1+len(bs.Sum2)+8+4)
		n := 0

		binary.BigEndian.PutUint32(buf[n:], uint32(bs.Index))
		n += 4
		binary.BigEndian.PutUint32(buf[n:], bs.Sum1)
		n += 4
		buf[n] = byte(len(bs.Sum2))
		n += 1
		copy(buf[n:], bs.Sum2)
		n += len(bs.Sum2)
		binary.BigEndian.PutUint64(buf[n:], uint64(bs.Offset))
		n += 8
		binary.BigEndian.PutUint32(buf[n:], uint32(bs.Length))
		n += 4

		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	return nil
}


func WireDecodeSignature(r io.Reader) (*Signature, error) {
	header := make([]byte, 16)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("读取签名 %w", err)
	}

	sig := &Signature{
		BlockSize: int32(binary.BigEndian.Uint32(header[0:4])),
		FileSize:  int64(binary.BigEndian.Uint64(header[4:12])),
	}
	count := binary.BigEndian.Uint32(header[12:16])
	sig.BlockSums = make([]BlockSum, count)

	for i := uint32(0); i < count; i++ {
		// 读取固定部分: index+sum1+sum2Len = 9 bytes
		fixed := make([]byte, 9)
		if _, err := io.ReadFull(r, fixed); err != nil {
			return nil, fmt.Errorf("读取%d: %w", i, err)
		}

		bs := BlockSum{
			Index: int(binary.BigEndian.Uint32(fixed[0:4])),
			Sum1:  binary.BigEndian.Uint32(fixed[4:8]),
		}
		sum2Len := int(fixed[8])

		bs.Sum2 = make([]byte, sum2Len)
		if _, err := io.ReadFull(r, bs.Sum2); err != nil {
			return nil, fmt.Errorf("读取%d sum2: %w", i, err)
		}


		tail := make([]byte, 12)
		if _, err := io.ReadFull(r, tail); err != nil {
			return nil, fmt.Errorf("读取%d tail: %w", i, err)
		}
		bs.Offset = int64(binary.BigEndian.Uint64(tail[0:8]))
		bs.Length = int32(binary.BigEndian.Uint32(tail[8:12]))

		sig.BlockSums[i] = bs
	}

	return sig, nil
}

// WireEncodeInstructions 将指令序列编码为二进制流
func WireEncodeInstructions(w io.Writer, insts []MatchResult) error {
	// count(4)
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, uint32(len(insts)))
	if _, err := w.Write(count); err != nil {
		return err
	}

	for _, inst := range insts {
		if inst.IsLiteral {
			// flag(1) + dataLen(4) + data
			header := make([]byte, 5)
			header[0] = 0 // literal
			binary.BigEndian.PutUint32(header[1:], uint32(len(inst.Data)))
			if _, err := w.Write(header); err != nil {
				return err
			}
			if _, err := w.Write(inst.Data); err != nil {
				return err
			}
		} else {
			// flag(1) + blockIdx(4)
			header := make([]byte, 5)
			header[0] = 1 // match
			binary.BigEndian.PutUint32(header[1:], uint32(inst.BlockIdx))
			if _, err := w.Write(header); err != nil {
				return err
			}
		}
	}
	return nil
}


func WireDecodeInstructions(r io.Reader) ([]MatchResult, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("读取指令 %w", err)
	}

	count := binary.BigEndian.Uint32(header)
	insts := make([]MatchResult, count)

	for i := uint32(0); i < count; i++ {
		flag := make([]byte, 1)
		if _, err := io.ReadFull(r, flag); err != nil {
			return nil, fmt.Errorf("读取指令 %d flag: %w", i, err)
		}

		if flag[0] == 0 {
			// literal
			lenBuf := make([]byte, 4)
			if _, err := io.ReadFull(r, lenBuf); err != nil {
				return nil, fmt.Errorf("读取指令 %d len: %w", i, err)
			}
			dataLen := binary.BigEndian.Uint32(lenBuf)
			data := make([]byte, dataLen)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, fmt.Errorf("读取指令 %d data: %w", i, err)
			}
			insts[i] = MatchResult{IsLiteral: true, Data: data}
		} else {
			// match
			idxBuf := make([]byte, 4)
			if _, err := io.ReadFull(r, idxBuf); err != nil {
				return nil, fmt.Errorf("读取指令 %d idx: %w", i, err)
			}
			insts[i] = MatchResult{
				IsLiteral: false,
				BlockIdx:  int(binary.BigEndian.Uint32(idxBuf)),
			}
		}
	}

	return insts, nil
}
