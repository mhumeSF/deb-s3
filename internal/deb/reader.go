package deb

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const (
	DefaultMaxControlArchiveSize int64 = 64 << 20
	DefaultMaxControlFileSize    int64 = 8 << 20
)

type ReaderOptions struct {
	MaxControlArchiveSize int64
	MaxControlFileSize    int64
}

type PackageInfo struct {
	Package *apt.Package
	Size    int64
	MD5     string
	SHA1    string
	SHA256  string
}

func ReadPackage(ctx context.Context, reader io.Reader, options ReaderOptions) (*PackageInfo, error) {
	options = options.withDefaults()
	md5Hash := md5.New()
	sha1Hash := sha1.New()
	sha256Hash := sha256.New()
	counter := &countingWriter{}
	stream := io.TeeReader(&contextReader{ctx: ctx, reader: reader}, io.MultiWriter(md5Hash, sha1Hash, sha256Hash, counter))

	archive := newARReader(stream)
	var control []byte
	seenDebianBinary := false
	seenControl := false
	seenData := false
	state := 0

	for {
		member, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		switch state {
		case 0:
			if member.Name != "debian-binary" {
				return nil, &FormatError{Reason: fmt.Sprintf("first member is %q instead of debian-binary", member.Name)}
			}
			if err := validateDebianBinary(member); err != nil {
				return nil, err
			}
			seenDebianBinary = true
			state = 1
		case 1:
			if strings.HasPrefix(member.Name, "_") {
				continue
			}
			if !strings.HasPrefix(member.Name, "control.tar") {
				return nil, &FormatError{Reason: fmt.Sprintf("unexpected member %q before control archive", member.Name)}
			}
			control, err = readControlArchive(member, options)
			if err != nil {
				return nil, err
			}
			seenControl = true
			state = 2
		case 2:
			if strings.HasPrefix(member.Name, "_") {
				continue
			}
			if !strings.HasPrefix(member.Name, "data.tar") {
				return nil, &FormatError{Reason: fmt.Sprintf("unexpected member %q before data archive", member.Name)}
			}
			seenData = true
			state = 3
		default:
			// Future trailing members are explicitly allowed by the format.
		}
	}

	if !seenDebianBinary {
		return nil, &FormatError{Reason: "missing debian-binary member"}
	}
	if !seenControl {
		return nil, &MissingMemberError{Member: "control archive", err: ErrMissingControlArchive}
	}
	if !seenData {
		return nil, &MissingMemberError{Member: "data archive", err: ErrMissingDataArchive}
	}
	pack, err := apt.ParsePackage(string(control))
	if err != nil {
		return nil, fmt.Errorf("parse package control metadata: %w", err)
	}
	return &PackageInfo{
		Package: pack,
		Size:    counter.size,
		MD5:     hex.EncodeToString(md5Hash.Sum(nil)),
		SHA1:    hex.EncodeToString(sha1Hash.Sum(nil)),
		SHA256:  hex.EncodeToString(sha256Hash.Sum(nil)),
	}, nil
}

func ReadPackageFile(ctx context.Context, filename string, options ReaderOptions) (*PackageInfo, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open Debian package %q: %w", filename, err)
	}
	defer file.Close()
	info, err := ReadPackage(ctx, file, options)
	if err != nil {
		return nil, fmt.Errorf("read Debian package %q: %w", filename, err)
	}
	info.Package.Filename = filename
	size := strconv.FormatInt(info.Size, 10)
	md5Value := info.MD5
	sha1Value := info.SHA1
	sha256Value := info.SHA256
	info.Package.Size = &size
	info.Package.MD5 = &md5Value
	info.Package.SHA1 = &sha1Value
	info.Package.SHA256 = &sha256Value
	return info, nil
}

func validateDebianBinary(member *arMember) error {
	if member.Size > 32 {
		return &FormatError{Reason: "debian-binary member is unexpectedly large"}
	}
	value, err := io.ReadAll(member.Body)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &FormatError{Reason: "truncated debian-binary member"}
		}
		return err
	}
	version := strings.TrimSpace(string(value))
	major, _, found := strings.Cut(version, ".")
	if !found || major != "2" {
		return &FormatError{Reason: fmt.Sprintf("unsupported Debian package format version %q", version)}
	}
	return nil
}

func readControlArchive(member *arMember, options ReaderOptions) ([]byte, error) {
	reader, closeReader, err := decompressControl(member.Name, member.Body)
	if err != nil {
		return nil, err
	}
	if closeReader != nil {
		defer closeReader()
	}
	limited := &sizeLimitedReader{reader: reader, remaining: options.MaxControlArchiveSize, limit: options.MaxControlArchiveSize}
	return readControlTar(limited, options.MaxControlFileSize)
}

func decompressControl(name string, reader io.Reader) (io.Reader, func(), error) {
	switch name {
	case "control.tar":
		return reader, nil, nil
	case "control.tar.gz":
		compressed, err := gzip.NewReader(reader)
		if err != nil {
			return nil, nil, &FormatError{Reason: fmt.Sprintf("invalid gzip control archive: %v", err)}
		}
		return compressed, func() { _ = compressed.Close() }, nil
	case "control.tar.xz":
		compressed, err := xz.NewReader(reader)
		if err != nil {
			return nil, nil, &FormatError{Reason: fmt.Sprintf("invalid xz control archive: %v", err)}
		}
		return compressed, nil, nil
	case "control.tar.zst":
		compressed, err := zstd.NewReader(reader)
		if err != nil {
			return nil, nil, &FormatError{Reason: fmt.Sprintf("invalid zstd control archive: %v", err)}
		}
		return compressed, compressed.Close, nil
	case "control.tar.bz2":
		return bzip2.NewReader(reader), nil, nil
	default:
		return nil, nil, &UnsupportedCompressionError{Member: name}
	}
}

func readControlTar(reader io.Reader, maxControlFileSize int64) ([]byte, error) {
	archive := tar.NewReader(reader)
	var control []byte
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, &FormatError{Reason: fmt.Sprintf("invalid control tar archive: %v", err)}
		}
		cleanName, err := safeTarName(header.Name)
		if err != nil {
			return nil, err
		}
		if cleanName != "control" {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, &FormatError{Reason: "control tar member is not a regular file"}
		}
		if header.Size > maxControlFileSize {
			return nil, &SizeLimitError{Limit: maxControlFileSize}
		}
		value, err := io.ReadAll(io.LimitReader(archive, maxControlFileSize+1))
		if err != nil {
			if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, &FormatError{Reason: fmt.Sprintf("read control file: %v", err)}
		}
		if int64(len(value)) > maxControlFileSize {
			return nil, &SizeLimitError{Limit: maxControlFileSize}
		}
		control = value
	}
	if control == nil {
		return nil, &MissingMemberError{Member: "control file", err: ErrMissingControlFile}
	}
	return control, nil
}

func safeTarName(name string) (string, error) {
	if name == "" || path.IsAbs(name) {
		return "", &FormatError{Reason: fmt.Sprintf("unsafe control tar path %q", name)}
	}
	cleaned := path.Clean(strings.TrimPrefix(name, "./"))
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", &FormatError{Reason: fmt.Sprintf("unsafe control tar path %q", name)}
	}
	return cleaned, nil
}

func (options ReaderOptions) withDefaults() ReaderOptions {
	if options.MaxControlArchiveSize <= 0 {
		options.MaxControlArchiveSize = DefaultMaxControlArchiveSize
	}
	if options.MaxControlFileSize <= 0 {
		options.MaxControlFileSize = DefaultMaxControlFileSize
	}
	return options
}

type countingWriter struct {
	size int64
}

func (w *countingWriter) Write(value []byte) (int, error) {
	w.size += int64(len(value))
	return len(value), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}

type sizeLimitedReader struct {
	reader    io.Reader
	remaining int64
	limit     int64
}

func (r *sizeLimitedReader) Read(value []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, &SizeLimitError{Limit: r.limit}
	}
	if int64(len(value)) > r.remaining {
		value = value[:r.remaining]
	}
	n, err := r.reader.Read(value)
	r.remaining -= int64(n)
	return n, err
}
