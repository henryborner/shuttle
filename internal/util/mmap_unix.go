//go:build !windows

package util

import (
	"fmt"
	"os"
	"syscall"
)

func mmap(f *os.File, size int64) ([]byte, error) {
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap: %w", err)
	}
	return data, nil
}

func munmap(data []byte) error {
	return syscall.Munmap(data)
}
