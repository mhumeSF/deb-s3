package deb

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidArchive         = errors.New("invalid Debian package archive")
	ErrMissingControlArchive  = errors.New("control archive not found")
	ErrMissingControlFile     = errors.New("control file not found")
	ErrMissingDataArchive     = errors.New("data archive not found")
	ErrUnsupportedCompression = errors.New("unsupported control archive compression")
	ErrArchiveTooLarge        = errors.New("control archive exceeds size limit")
)

type FormatError struct {
	Reason string
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("invalid Debian package archive: %s", e.Reason)
}

func (e *FormatError) Unwrap() error { return ErrInvalidArchive }

type MissingMemberError struct {
	Member string
	err    error
}

func (e *MissingMemberError) Error() string {
	return fmt.Sprintf("Debian package is missing %s", e.Member)
}

func (e *MissingMemberError) Unwrap() error { return e.err }

type UnsupportedCompressionError struct {
	Member string
}

func (e *UnsupportedCompressionError) Error() string {
	return fmt.Sprintf("unsupported control archive compression in %q", e.Member)
}

func (e *UnsupportedCompressionError) Unwrap() error { return ErrUnsupportedCompression }

type SizeLimitError struct {
	Limit int64
}

func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("control archive exceeds %d-byte size limit", e.Limit)
}

func (e *SizeLimitError) Unwrap() error { return ErrArchiveTooLarge }
