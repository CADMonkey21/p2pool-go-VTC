//go:build windows

package verthash

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mmapReadOnly is the Windows equivalent of mmap(PROT_READ, MAP_SHARED).
func mmapReadOnly(f *os.File, size int64) ([]byte, error) {
	h, err := windows.CreateFileMapping(windows.Handle(f.Fd()), nil, windows.PAGE_READONLY,
		uint32(uint64(size)>>32), uint32(size), nil)
	if err != nil {
		return nil, err
	}
	// The view keeps the mapping alive, so the mapping handle can be closed right away.
	defer windows.CloseHandle(h)

	addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, err
	}
	// addr is OS-owned memory outside the Go heap; convert without tripping vet's unsafeptr check.
	return unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), int(size)), nil
}

func munmap(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return windows.UnmapViewOfFile(uintptr(unsafe.Pointer(&b[0])))
}
