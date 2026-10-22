//go:build !unix && !windows

package verthash

import (
	"errors"
	"os"
)

// mmapReadOnly always fails here, so NewVerthash falls back to os.ReadFile.
func mmapReadOnly(f *os.File, size int64) ([]byte, error) {
	return nil, errors.New("mmap not supported on this platform")
}

func munmap(b []byte) error { return nil }
