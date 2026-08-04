package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("object not found")
	ErrConflict = errors.New("object already exists with different contents")
)

// Store is the repository's object-storage boundary. Keys passed to and
// returned from a Store are logical repository keys; implementations hide any
// configured bucket prefix.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, options PutOptions) error
	Delete(ctx context.Context, key string) error
	Copy(ctx context.Context, source, destination string, options CopyOptions) error
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// ConditionalDeleter is implemented by stores that can atomically delete an
// object only when its current ETag matches. Repository locking uses this to
// avoid deleting a replacement owner's lock.
type ConditionalDeleter interface {
	DeleteIfMatch(ctx context.Context, key, etag string) error
}

type ObjectInfo struct {
	Key                  string
	Size                 int64
	ETag                 string
	Metadata             map[string]string
	ContentType          string
	CacheControl         string
	ACL                  string
	ServerSideEncryption bool
	LastModified         time.Time
}

type PutOptions struct {
	ContentType          string
	CacheControl         string
	Metadata             map[string]string
	FailIfExists         bool
	CreateOnly           bool
	ServerSideEncryption bool
}

type CopyOptions struct {
	SourceETag   string
	CreateOnly   bool
	FailIfExists bool
}

type NotFoundError struct {
	Key string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("object %q not found", e.Key)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

type ConflictError struct {
	Key string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("object %q already exists with different contents", e.Key)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// JoinKey combines a configured S3 prefix with a logical repository key. It
// deliberately uses path semantics on every platform.
func JoinKey(prefix, key string) (string, error) {
	prefix, err := normalizePrefix(prefix)
	if err != nil {
		return "", err
	}
	key = strings.TrimPrefix(key, "/")
	if key == "" || key == "." {
		return "", errors.New("object key cannot be empty")
	}
	if strings.Contains(key, "\\") {
		return "", fmt.Errorf("object key %q contains a backslash", key)
	}
	cleaned := path.Clean(key)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("object key %q escapes the repository prefix", key)
	}
	if prefix == "" {
		return cleaned, nil
	}
	return path.Join(prefix, cleaned), nil
}

func normalizePrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	if strings.Contains(prefix, "\\") {
		return "", fmt.Errorf("object prefix %q contains a backslash", prefix)
	}
	cleaned := path.Clean(prefix)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("object prefix %q escapes the bucket root", prefix)
	}
	return cleaned, nil
}

func logicalKey(prefix, physicalKey string) (string, bool) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return physicalKey, true
	}
	want := prefix + "/"
	if !strings.HasPrefix(physicalKey, want) {
		return "", false
	}
	return strings.TrimPrefix(physicalKey, want), true
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
