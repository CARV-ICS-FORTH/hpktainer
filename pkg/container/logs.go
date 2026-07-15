// Copyright © 2023 FORTH-ICS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// The code is adapted from Podman 2023.

package container

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	// LogTimeFormat is the time format used in the log.
	// It is a modified version of RFC3339Nano that guarantees trailing
	// zeroes are not trimmed, taken from
	// https://github.com/golang/go/issues/19635
	LogTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

	// PartialLogType signifies a log line that exceeded the buffer
	// length and needed to spill into a new line
	PartialLogType = "P"

	// FullLogType signifies a log line is full
	FullLogType = "F"

	// ANSIEscapeResetCode is a code that resets all colors and text effects
	ANSIEscapeResetCode = "\033[0m"
)



func GetTailLog(path string, tail int) ([]string, error) {
	var (
		nllCounter int
		leftover   string
		tailLog    []string
	)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rr, err := NewReverseReader(f)
	if err != nil {
		return nil, err
	}

	inputs := make(chan []string)
	go func() {
		for {
			s, err := rr.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					inputs <- []string{leftover}
				} else {
					logrus.Error(err)
				}
				close(inputs)
				if err := f.Close(); err != nil {
					logrus.Error(err)
				}
				break
			}
			line := strings.Split(s+leftover, "\n")
			if len(line) > 1 {
				inputs <- line[1:]
			}
			leftover = line[0]
		}
	}()

	for i := range inputs {
		// the incoming array is FIFO; we want FIFO so
		// reverse the slice read order
		for j := len(i) - 1; j >= 0; j-- {
			// lines that are "" are junk
			if len(i[j]) < 1 {
				continue
			}
			// read the content in reverse and add each nll until we have the same
			// number of F type messages as the desired tail
			tailLog = append(tailLog, i[j])
			nllCounter++
			if nllCounter >= tail {
				break
			}
		}
		// if we have enough log lines, we can hang up
		if nllCounter >= tail {
			break
		}
	}

	// Reverse tailLog so lines are returned in chronological order
	for k, l := 0, len(tailLog)-1; k < l; k, l = k+1, l-1 {
		tailLog[k], tailLog[l] = tailLog[l], tailLog[k]
	}

	return tailLog, nil
}



// ReverseReader structure for reading a file backwards
type ReverseReader struct {
	reader   *os.File
	offset   int64
	readSize int64
}

// NewReverseReader returns a reader that reads from the end of a file
// rather than the beginning.  It sets the readsize to pagesize and determines
// the first offset using modulus.
func NewReverseReader(reader *os.File) (*ReverseReader, error) {
	// pagesize should be safe for memory use and file reads should be on page
	// boundaries as well
	pageSize := int64(os.Getpagesize())
	stat, err := reader.Stat()
	if err != nil {
		return nil, err
	}
	// figure out the last page boundary
	remainder := stat.Size() % pageSize
	end, err := reader.Seek(0, 2)
	if err != nil {
		return nil, err
	}
	// set offset (starting position) to the last page boundary or
	// zero if fits in one page
	startOffset := end - remainder
	if startOffset < 0 {
		startOffset = 0
	}
	rr := ReverseReader{
		reader:   reader,
		offset:   startOffset,
		readSize: pageSize,
	}
	return &rr, nil
}

// ReverseReader reads from a given offset to the previous offset and
// then sets the newoff set one pagesize less than the previous read.
func (r *ReverseReader) Read() (string, error) {
	if r.offset < 0 {
		return "", fmt.Errorf("at beginning of file: %w", io.EOF)
	}
	// Read from given offset
	b := make([]byte, r.readSize)
	n, err := r.reader.ReadAt(b, r.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if int64(n) < r.readSize {
		b = b[0:n]
	}
	// Move the offset one pagesize up
	r.offset -= r.readSize
	return string(b), nil
}
