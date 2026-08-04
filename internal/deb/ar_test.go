package deb

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestARReaderHandlesOddMemberPadding(t *testing.T) {
	archive := buildAR(t,
		testARMember{name: "odd", body: []byte("123")},
		testARMember{name: "even", body: []byte("4567")},
	)
	reader := newARReader(bytes.NewReader(archive))
	first, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	value, err := io.ReadAll(first.Body)
	if err != nil || first.Name != "odd" || string(value) != "123" {
		t.Fatalf("first member = %#v, %q, %v", first, value, err)
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	value, err = io.ReadAll(second.Body)
	if err != nil || second.Name != "even" || string(value) != "4567" {
		t.Fatalf("second member = %#v, %q, %v", second, value, err)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final Next() = %v, want EOF", err)
	}
}

func TestARReaderRejectsMalformedArchives(t *testing.T) {
	tests := map[string][]byte{
		"bad magic":        []byte("not ar\n"),
		"truncated header": append([]byte(arMagic), []byte("short")...),
		"bad trailer": func() []byte {
			archive := buildAR(t, testARMember{name: "member", body: nil})
			archive[len(arMagic)+58] = 'x'
			return archive
		}(),
		"bad size": func() []byte {
			archive := buildAR(t, testARMember{name: "member", body: nil})
			copy(archive[len(arMagic)+48:len(arMagic)+58], []byte("not-a-size"))
			return archive
		}(),
	}
	for name, archive := range tests {
		t.Run(name, func(t *testing.T) {
			reader := newARReader(bytes.NewReader(archive))
			if _, err := reader.Next(); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("Next() error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}
