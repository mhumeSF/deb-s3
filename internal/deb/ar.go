package deb

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	arMagic      = "!<arch>\n"
	arHeaderSize = 60
)

type arReader struct {
	reader    io.Reader
	started   bool
	remaining int64
	padding   int64
}

type arMember struct {
	Name string
	Size int64
	Body io.Reader
}

func newARReader(reader io.Reader) *arReader {
	return &arReader{reader: reader}
}

func (r *arReader) Next() (*arMember, error) {
	if !r.started {
		magic := make([]byte, len(arMagic))
		if _, err := io.ReadFull(r.reader, magic); err != nil {
			return nil, formatReadError(err, "missing ar magic")
		}
		if string(magic) != arMagic {
			return nil, &FormatError{Reason: "incorrect ar magic"}
		}
		r.started = true
	}

	if r.remaining > 0 {
		if _, err := io.CopyN(io.Discard, r.reader, r.remaining); err != nil {
			return nil, formatReadError(err, "truncated ar member")
		}
	}
	if r.padding > 0 {
		padding := make([]byte, r.padding)
		if _, err := io.ReadFull(r.reader, padding); err != nil {
			return nil, formatReadError(err, "truncated ar member padding")
		}
	}
	r.remaining = 0
	r.padding = 0

	header := make([]byte, arHeaderSize)
	n, err := io.ReadFull(r.reader, header)
	if errors.Is(err, io.EOF) && n == 0 {
		return nil, io.EOF
	}
	if err != nil {
		return nil, formatReadError(err, "truncated ar member header")
	}
	if string(header[58:60]) != "`\n" {
		return nil, &FormatError{Reason: "invalid ar member header trailer"}
	}

	name := strings.TrimSpace(string(header[0:16]))
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		return nil, &FormatError{Reason: "ar member has an empty name"}
	}
	if name == "/" || name == "//" || strings.HasPrefix(name, "#1/") {
		return nil, &FormatError{Reason: fmt.Sprintf("unsupported extended ar member name %q", name)}
	}
	sizeText := strings.TrimSpace(string(header[48:58]))
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return nil, &FormatError{Reason: fmt.Sprintf("invalid size for ar member %q", name)}
	}

	r.remaining = size
	r.padding = size % 2
	body := &memberReader{archive: r}
	return &arMember{Name: name, Size: size, Body: body}, nil
}

func formatReadError(err error, reason string) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &FormatError{Reason: reason}
	}
	return err
}

type memberReader struct {
	archive *arReader
}

func (r *memberReader) Read(buffer []byte) (int, error) {
	if r.archive.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > r.archive.remaining {
		buffer = buffer[:r.archive.remaining]
	}
	n, err := r.archive.reader.Read(buffer)
	r.archive.remaining -= int64(n)
	if errors.Is(err, io.EOF) && r.archive.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
