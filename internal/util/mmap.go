// mmap.go — cross-platform memory-mapped file I/O
// Avoids loading entire file into RAM for checksum/delta operations.
// mmap.go — 跨平台内存映射文件 I/O，避免将整个文件加载到 RAM 中。
package util

import (
	"fmt"
	"os"
	"runtime"
)

// MmapFile maps a file into memory read-only.
// Returns a []byte spanning the entire file. The OS pages data on demand.
// MmapFile 将文件以只读方式映射到内存。OS 按需换页，内存占用与工作集而非文件大小成正比。
func MmapFile(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("mmap stat: %w", err)
	}
	size := fi.Size()
	if size == 0 {
		return []byte{}, nil
	}
	return mmap(f, size)
}

// Munmap unmaps the memory region previously mapped by MmapFile.
// Munmap 解除 MmapFile 建立的内存映射。
func Munmap(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return munmap(data)
}

// MmapReadOnly opens and mmaps a file by path, returning data + a closer function.
// The closer handles both munmap and file close; call it when done.
// MmapReadOnly 按路径打开并 mmap 文件，返回数据 + 清理函数（处理 munmap + 关闭文件）。
func MmapReadOnly(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap open %s: %w", path, err)
	}
	data, err := MmapFile(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	close := func() error {
		e1 := Munmap(data)
		e2 := f.Close()
		if e1 != nil {
			return e1
		}
		return e2
	}
	// Keep f alive: the mmap holds a reference to the file descriptor.
	// The runtime finalizer on the returned closer ensures cleanup.
	runtime.KeepAlive(f)
	return data, close, nil
}
