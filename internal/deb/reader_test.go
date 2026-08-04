package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const testControl = "Package: example\nVersion: 2:1.0-3\nArchitecture: amd64\nDescription: example package\n"

type testARMember struct {
	name string
	body []byte
}

func TestReadPackageCompressionFormats(t *testing.T) {
	controlTar := buildControlTar(t, "./control", tar.TypeReg, []byte(testControl), nil)
	formats := map[string]func(*testing.T, []byte) []byte{
		"control.tar":     func(_ *testing.T, value []byte) []byte { return value },
		"control.tar.gz":  compressGzip,
		"control.tar.xz":  compressXZ,
		"control.tar.zst": compressZstd,
	}
	for memberName, compress := range formats {
		t.Run(memberName, func(t *testing.T) {
			archive := buildDeb(t, memberName, compress(t, controlTar))
			assertReadPackage(t, archive, "example")
		})
	}
}

func TestReadPackageBzip2Extension(t *testing.T) {
	encoded := strings.ReplaceAll(`QlpoOTFBWSZTWUh9VSUAAET/lMoQAIBAA3+QIABBIHrv3tAEAAACAAggAHQamk2qfompoaHpDRoG
TRtpQZSnoymDTUwAAmmJgwLyeyFOgAwOMQPyUrdICCPaLkmYtWxQLUAGpbSZEg4IElBxzbJDfcRX
OlDVjaJKRi/G1dG9PNI9XZJ8Bj36hQZqjUE9avFQsminpTdF9VkL60XPKunZjP4u5IpwoSCQ+qpK`, "\n", "")
	controlArchive, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	archive := buildDeb(t, "control.tar.bz2", controlArchive)
	assertReadPackage(t, archive, "bz2-example")
}

func TestReadPackageFileAppliesFileMetadata(t *testing.T) {
	archive := buildDeb(t, "control.tar.gz", compressGzip(t, buildControlTar(t, "control", tar.TypeReg, []byte(testControl), nil)))
	filename := filepath.Join(t.TempDir(), "example_1.0_amd64.deb")
	if err := os.WriteFile(filename, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadPackageFile(context.Background(), filename, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Package.Filename != filename || info.Package.Size == nil || *info.Package.Size != fmt.Sprint(len(archive)) {
		t.Fatalf("file metadata package = %#v", info.Package)
	}
	if info.Package.MD5 == nil || *info.Package.MD5 != info.MD5 || info.Package.SHA1 == nil || *info.Package.SHA1 != info.SHA1 || info.Package.SHA256 == nil || *info.Package.SHA256 != info.SHA256 {
		t.Fatalf("package hashes do not match result: %#v", info.Package)
	}
	poolPath, err := info.Package.RepositoryFilename("stable")
	if err != nil || poolPath != "pool/stable/e/ex/example_1.0_amd64.deb" {
		t.Fatalf("RepositoryFilename() = %q, %v", poolPath, err)
	}
}

func TestReadPackageAllowsIgnorableAndTrailingMembers(t *testing.T) {
	controlArchive := buildControlTar(t, "control", tar.TypeReg, []byte(testControl), nil)
	archive := buildAR(t,
		testARMember{name: "debian-binary", body: []byte("2.0\n")},
		testARMember{name: "_future", body: []byte("ignored")},
		testARMember{name: "control.tar", body: controlArchive},
		testARMember{name: "_between", body: []byte("ignored")},
		testARMember{name: "data.tar", body: emptyTar(t)},
		testARMember{name: "trailing", body: []byte("future")},
	)
	assertReadPackage(t, archive, "example")
}

func TestReadPackageErrors(t *testing.T) {
	validControl := buildControlTar(t, "control", tar.TypeReg, []byte(testControl), nil)
	tests := []struct {
		name    string
		archive []byte
		target  error
	}{
		{name: "bad magic", archive: []byte("not-a-deb"), target: ErrInvalidArchive},
		{name: "wrong format version", archive: buildAR(t, testARMember{name: "debian-binary", body: []byte("3.0\n")}), target: ErrInvalidArchive},
		{name: "missing control", archive: buildAR(t, testARMember{name: "debian-binary", body: []byte("2.0\n")}), target: ErrMissingControlArchive},
		{name: "missing data", archive: buildAR(t, testARMember{name: "debian-binary", body: []byte("2.0\n")}, testARMember{name: "control.tar", body: validControl}), target: ErrMissingDataArchive},
		{name: "missing control file", archive: buildDeb(t, "control.tar", emptyTar(t)), target: ErrMissingControlFile},
		{name: "unsupported compression", archive: buildDeb(t, "control.tar.lzma", []byte("compressed")), target: ErrUnsupportedCompression},
		{name: "control is symlink", archive: buildDeb(t, "control.tar", buildControlTar(t, "control", tar.TypeSymlink, nil, nil)), target: ErrInvalidArchive},
		{name: "unsafe tar path", archive: buildDeb(t, "control.tar", buildControlTar(t, "../control", tar.TypeReg, []byte(testControl), nil)), target: ErrInvalidArchive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadPackage(context.Background(), bytes.NewReader(tt.archive), ReaderOptions{})
			if !errors.Is(err, tt.target) {
				t.Fatalf("ReadPackage() error = %v, want %v", err, tt.target)
			}
		})
	}
}

func TestReadPackageSizeLimits(t *testing.T) {
	controlArchive := buildControlTar(t, "control", tar.TypeReg, []byte(testControl), nil)
	archive := buildDeb(t, "control.tar", controlArchive)
	for name, options := range map[string]ReaderOptions{
		"archive": {MaxControlArchiveSize: 512, MaxControlFileSize: DefaultMaxControlFileSize},
		"file":    {MaxControlArchiveSize: DefaultMaxControlArchiveSize, MaxControlFileSize: 8},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadPackage(context.Background(), bytes.NewReader(archive), options)
			if !errors.Is(err, ErrArchiveTooLarge) {
				t.Fatalf("ReadPackage() error = %v, want ErrArchiveTooLarge", err)
			}
		})
	}
}

func TestReadPackageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadPackage(ctx, bytes.NewReader(buildDeb(t, "control.tar", emptyTar(t))), ReaderOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadPackage() error = %v, want context.Canceled", err)
	}
}

func assertReadPackage(t *testing.T, archive []byte, packageName string) {
	t.Helper()
	info, err := ReadPackage(context.Background(), bytes.NewReader(archive), ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Package.Name != packageName || info.Package.FullVersion() == "" {
		t.Fatalf("Package = %#v", info.Package)
	}
	if info.Size != int64(len(archive)) {
		t.Fatalf("Size = %d, want %d", info.Size, len(archive))
	}
	md5Value := fmt.Sprintf("%x", md5.Sum(archive))
	sha1Value := fmt.Sprintf("%x", sha1.Sum(archive))
	sha256Value := fmt.Sprintf("%x", sha256.Sum256(archive))
	if info.MD5 != md5Value || info.SHA1 != sha1Value || info.SHA256 != sha256Value {
		t.Fatalf("hashes = %s %s %s; want %s %s %s", info.MD5, info.SHA1, info.SHA256, md5Value, sha1Value, sha256Value)
	}
}

func buildDeb(t *testing.T, controlName string, controlArchive []byte) []byte {
	t.Helper()
	return buildAR(t,
		testARMember{name: "debian-binary", body: []byte("2.0\n")},
		testARMember{name: controlName, body: controlArchive},
		testARMember{name: "data.tar", body: emptyTar(t)},
	)
}

func buildAR(t *testing.T, members ...testARMember) []byte {
	t.Helper()
	var output bytes.Buffer
	output.WriteString(arMagic)
	for _, member := range members {
		name := member.name
		if len(name) < 16 {
			name += "/"
		}
		if len(name) > 16 {
			t.Fatalf("ar member name %q is too long", member.name)
		}
		header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n", name, 0, 0, 0, 0o644, len(member.body))
		if len(header) != arHeaderSize {
			t.Fatalf("ar header length = %d", len(header))
		}
		output.WriteString(header)
		output.Write(member.body)
		if len(member.body)%2 != 0 {
			output.WriteByte('\n')
		}
	}
	return output.Bytes()
}

func buildControlTar(t *testing.T, name string, typeflag byte, control []byte, extra map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := tar.NewWriter(&output)
	writeTarEntry(t, archive, name, typeflag, control)
	for extraName, value := range extra {
		writeTarEntry(t, archive, extraName, tar.TypeReg, value)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func writeTarEntry(t *testing.T, archive *tar.Writer, name string, typeflag byte, value []byte) {
	t.Helper()
	header := &tar.Header{Name: name, Mode: 0o644, Typeflag: typeflag}
	if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
		header.Size = int64(len(value))
	}
	if typeflag == tar.TypeSymlink {
		header.Linkname = "target"
	}
	if err := archive.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if len(value) > 0 {
		if _, err := archive.Write(value); err != nil {
			t.Fatal(err)
		}
	}
}

func emptyTar(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := tar.NewWriter(&output)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func compressGzip(t *testing.T, value []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func compressXZ(t *testing.T, value []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer, err := xz.NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func compressZstd(t *testing.T, value []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer, err := zstd.NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

var _ io.Reader = (*contextReader)(nil)
