package protocol

import (
	"bytes"
	"errors"
	"io"
)

const MaxFrame = 4 << 20
const MaxStream = 16 << 20

type Frame struct {
	Raw      []byte
	Name, ID string
	Data     []byte
	HasData  bool
}

// ScanSSE delivers complete wire frames (including comments). Raw and Data are
// valid during emit. Delayed CR processing preserves CRLF across Read boundaries.
func ScanSSE(r io.Reader, emit func(Frame) error) error {
	var raw []byte
	var start, total int
	var pendingCR bool
	first := true
	var f Frame
	finishLine := func(end int) error {
		line := raw[start:end]
		start = len(raw)

		if first {
			first = false
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
		}

		if len(line) == 0 {
			f.Raw = raw
			if err := emit(f); err != nil {
				return err
			}

			raw = raw[:0]
			start = 0
			f = Frame{ID: f.ID}

			return nil
		}

		key, val, _ := bytes.Cut(line, []byte(":"))
		val = bytes.TrimPrefix(val, []byte(" "))

		switch string(key) {
		case "data":
			if f.HasData {
				f.Data = append(f.Data, '\n')
			}

			f.HasData = true
			f.Data = append(f.Data, val...)
		case "event":
			f.Name = string(val)
		case "id":
			if bytes.IndexByte(val, 0) < 0 {
				f.ID = string(val)
			}
		}

		return nil
	}
	var buf [32 << 10]byte

	for {
		n, err := r.Read(buf[:])
		for _, c := range buf[:n] {
			total++
			if total > MaxStream {
				return errors.New("SSE stream exceeds 16 MiB")
			}

			if pendingCR {
				pendingCR = false
				end := len(raw) - 1

				if c == '\n' {
					if len(raw) == MaxFrame {
						return errors.New("SSE frame exceeds 4 MiB")
					}

					raw = append(raw, c)
				}

				if e := finishLine(end); e != nil {
					return e
				}

				if c == '\n' {
					continue
				}
			}

			if len(raw) == MaxFrame {
				return errors.New("SSE frame exceeds 4 MiB")
			}

			raw = append(raw, c)

			switch c {
			case '\r':
				pendingCR = true
			case '\n':
				if e := finishLine(len(raw) - 1); e != nil {
					return e
				}
			}
		}

		if err != nil {
			if err != io.EOF {
				return err
			}

			if pendingCR {
				if e := finishLine(len(raw) - 1); e != nil {
					return e
				}
			}

			if len(raw) > 0 {
				return io.ErrUnexpectedEOF
			}

			return nil
		}
	}
}
