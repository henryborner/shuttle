//go:build windows

package util

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileMappingW = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile      = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile    = kernel32.NewProc("UnmapViewOfFile")
)

const (
	PAGE_READONLY = 0x02
	FILE_MAP_READ = 0x0004
)

func mmap(f *os.File, size int64) ([]byte, error) {
	if size > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("mmap: file too large")
	}

	// CreateFileMappingW: convert file handle to mapping object
	h, _, err := procCreateFileMappingW.Call(
		uintptr(f.Fd()), 0, PAGE_READONLY, 0, 0, 0,
	)
	if h == 0 {
		return nil, fmt.Errorf("mmap CreateFileMapping: %w", err)
	}

	// MapViewOfFile: map the entire file into address space
	addr, _, err := procMapViewOfFile.Call(
		h, FILE_MAP_READ, 0, 0, uintptr(size),
	)
	if addr == 0 {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, fmt.Errorf("mmap MapViewOfFile: %w", err)
	}
	syscall.CloseHandle(syscall.Handle(h))

	// Convert pointer to Go slice
	data := unsafe.Slice((*byte)(unsafe.Pointer(addr)), int(size))
	return data, nil
}

func munmap(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	addr := uintptr(unsafe.Pointer(unsafe.SliceData(data)))
	_, _, err := procUnmapViewOfFile.Call(addr)
	if err != syscall.Errno(0) {
		return fmt.Errorf("mmap UnmapViewOfFile: %w", err)
	}
	return nil
}
