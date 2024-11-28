// * Copyright 2003-2005 Colin Percival
// * All rights reserved
// *
// * Redistribution and use in source and binary forms, with or without
// * modification, are permitted providing that the following conditions
// * are met:
// * 1. Redistributions of source code must retain the above copyright
// *    notice, this list of conditions and the following disclaimer.
// * 2. Redistributions in binary form must reproduce the above copyright
// *    notice, this list of conditions and the following disclaimer in the
// *    documentation and/or other materials provided with the distribution.
// *
// * THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
// * IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
// * WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
// * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY
// * DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
// * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
// * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
// * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
// * STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING
// * IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
// * POSSIBILITY OF SUCH DAMAGE.

// Package bspatch is a binary diff program using suffix sorting.
package bspatch

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/dsnet/compress/bzip2"
	"github.com/gabstv/go-bsdiff/pkg/util"
)

// Bytes applies a patch with the oldfile to create the newfile
func Bytes(oldfile, patch []byte) (newfile []byte, err error) {
	var buf util.BufWriter
	err = patchb(bytes.NewReader(oldfile), bytes.NewReader(patch), &buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Reader applies a BSDIFF4 patch (using oldbin and patchf) to create the newbin
func Reader(oldfile io.ReaderAt, newfile io.WriterAt, patch io.ReaderAt) error {
	return patchb(oldfile, patch, newfile)
}

// File applies a BSDIFF4 patch (using oldfile and patchfile) to create the newfile
func File(oldfile, newfile, patchfile string) error {
	oldF, err := os.Open(oldfile)
	if err != nil {
		return fmt.Errorf("could not open oldfile '%v': %v", oldfile, err.Error())
	}
	defer oldF.Close()
	patchF, err := os.Open(patchfile)
	if err != nil {
		return fmt.Errorf("could not open patchfile '%v': %v", patchfile, err.Error())
	}
	defer patchF.Close()
	newF, err := os.OpenFile(newfile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("could not create newfile '%v': %v", newfile, err.Error())
	}
	err = patchb(oldF, patchF, newF)
	_ = newF.Close()
	if err != nil {
		os.Remove(newfile)
		return fmt.Errorf("bspatch: %v", err.Error())
	}
	return nil
}

func patchb(oldfile io.ReaderAt, patch io.ReaderAt, res io.WriterAt) error {
	var newsize int
	header := make([]byte, 32)
	buf := make([]byte, 8)
	var i int
	ctrl := make([]int, 3)

	f := io.NewSectionReader(patch, 0, int64(len(header)))

	//	File format:
	//		0	8	"BSDIFF40"
	//		8	8	X
	//		16	8	Y
	//		24	8	sizeof(newfile)
	//		32	X	bzip2(control block)
	//		32+X	Y	bzip2(diff block)
	//		32+X+Y	???	bzip2(extra block)
	//	with control block a set of triples (x,y,z) meaning "add x bytes
	//	from oldfile to x bytes from the diff block; copy y bytes from the
	//	extra block; seek forwards in oldfile by z bytes".

	// Read header
	if n, err := f.Read(header); err != nil || n < 32 {
		if err != nil {
			return fmt.Errorf("corrupt patch %v", err.Error())
		}
		return fmt.Errorf("corrupt patch (n %v < 32)", n)
	}
	// Check for appropriate magic
	if bytes.Compare(header[:8], []byte("BSDIFF40")) != 0 {
		return fmt.Errorf("corrupt patch (header BSDIFF40)")
	}

	// Read lengths from header
	bzctrllen := offtin(header[8:])
	bzdatalen := offtin(header[16:])
	newsize = offtin(header[24:])

	if bzctrllen < 0 || bzdatalen < 0 || newsize < 0 {
		return fmt.Errorf("corrupt patch (bzctrllen %v bzdatalen %v newsize %v)", bzctrllen, bzdatalen, newsize)
	}

	// Close patch file and re-open it via libbzip2 at the right places
	f = nil
	cpfbz2, err := bzip2.NewReader(io.NewSectionReader(patch, 32, int64(bzctrllen)), nil)
	if err != nil {
		return err
	}
	dpfbz2, err := bzip2.NewReader(io.NewSectionReader(patch, int64(32+bzctrllen), int64(bzdatalen)), nil)
	if err != nil {
		return err
	}
	epfbz2, err := bzip2.NewReader(io.NewSectionReader(patch, int64(32+bzctrllen+bzdatalen), 1<<31), nil)
	if err != nil {
		return err
	}

	// Preallocate required space
	if _, err = res.WriteAt([]byte{0}, int64(newsize-1)); err != nil {
		return err
	}

	const readBufSize = 64 * 1024
	var readBuf, readBufPatch [readBufSize]byte
	newpos := 0
	oldpos := 0

	for newpos < newsize {
		// Read control data
		for i = 0; i <= 2; i++ {
			lenread, err := io.ReadFull(cpfbz2, buf)
			if err != nil && err != io.EOF {
				e0 := ""
				if err != nil {
					e0 = err.Error()
				}
				return fmt.Errorf("corrupt patch or bzstream ended: %s (read: %v/8)", e0, lenread)
			}
			ctrl[i] = offtin(buf)
		}
		// Sanity-check
		if newpos+ctrl[0] > newsize {
			return fmt.Errorf("corrupt patch (sanity check)")
		}

		for i = 0; i < ctrl[0]; i += readBufSize {
			readSize := ctrl[0] - i
			if readSize > readBufSize {
				readSize = readBufSize
			}

			// Read diff string
			// lenread, err = dpfbz2.Read(pnew[newpos : newpos+ctrl[0]])
			_, err = io.ReadFull(dpfbz2, readBufPatch[:readSize])
			if err != nil && err != io.EOF {
				e0 := ""
				if err != nil {
					e0 = err.Error()
				}
				return fmt.Errorf("corrupt patch or bzstream ended (2): %s", e0)
			}

			// Add pold data to diff string
			n, _ := oldfile.ReadAt(readBuf[:readSize], int64(oldpos))
			for j := 0; j < n; j++ {
				readBufPatch[j] += readBuf[j]
			}

			if _, err = res.WriteAt(readBufPatch[:readSize], int64(newpos)); err != nil {
				return err
			}
			newpos += readSize
			oldpos += readSize
		}

		// Sanity-check
		if newpos+ctrl[1] > newsize {
			return fmt.Errorf("corrupt patch newpos+ctrl[1] newsize")
		}

		// Read extra string
		// epfbz2.Read was not reading all the requested bytes, probably an internal buffer limitation ?
		// it was encapsulated by zreadall to work around the issue
		for i = 0; i < ctrl[1]; i += readBufSize {
			readSize := ctrl[1] - i
			if readSize > readBufSize {
				readSize = readBufSize
			}
			if _, err = io.ReadFull(epfbz2, readBuf[:readSize]); err != nil && err != io.EOF {
				e0 := ""
				if err != nil {
					e0 = err.Error()
				}
				return fmt.Errorf("corrupt patch or bzstream ended (3): %s", e0)
			}
			if _, err = res.WriteAt(readBuf[:readSize], int64(newpos)); err != nil {
				return err
			}
			newpos += readSize
			oldpos += readSize
		}
		// Adjust pointers
		oldpos += ctrl[2] - ctrl[1]
	}

	// Clean up the bzip2 reads
	if err = cpfbz2.Close(); err != nil {
		return err
	}
	if err = dpfbz2.Close(); err != nil {
		return err
	}
	if err = epfbz2.Close(); err != nil {
		return err
	}

	return nil
}

// offtin reads an int64 (little endian)
func offtin(buf []byte) int {

	y := int(buf[7] & 0x7f)
	y = y * 256
	y += int(buf[6])
	y = y * 256
	y += int(buf[5])
	y = y * 256
	y += int(buf[4])
	y = y * 256
	y += int(buf[3])
	y = y * 256
	y += int(buf[2])
	y = y * 256
	y += int(buf[1])
	y = y * 256
	y += int(buf[0])

	if (buf[7] & 0x80) != 0 {
		y = -y
	}
	return y
}
