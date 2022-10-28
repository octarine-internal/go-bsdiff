package util

import (
	"fmt"
	"io"
)

const (
	buffersize = 1024 * 16
)

// BufWriter is byte slice buffer that implements io.WriteSeeker
type BufWriter struct {
	buf  []byte
	pos  int
}

func (m *BufWriter) WriteAt(p []byte, off int64) (n int, err error) {
	minCap := int(off) + len(p)
	if minCap > len(m.buf) {
		buf2 := make([]byte, minCap)
		copy(buf2, m.buf)
		m.buf = buf2
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

// Write the contents of p and return the bytes written
func (m *BufWriter) Write(p []byte) (n int, err error) {
	n, err = m.WriteAt(p, int64(m.pos))
	m.pos += n
	return n, err
}

// Seek to a position on the byte slice
func (m *BufWriter) Seek(offset int64, whence int) (int64, error) {
	newPos, offs := 0, int(offset)
	switch whence {
	case io.SeekStart:
		newPos = offs
	case io.SeekCurrent:
		newPos = m.pos + offs
	case io.SeekEnd:
		newPos = len(m.buf) + offs
	}
	if newPos < 0 {
		return 0, fmt.Errorf("negative result pos")
	}
	m.pos = newPos
	return int64(newPos), nil
}

// Len returns the length of the internal byte slice
func (m *BufWriter) Len() int {
	return len(m.buf)
}

// Bytes returns internal byte slice
func (m *BufWriter) Bytes() []byte {
	return m.buf
}
