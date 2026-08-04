package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu      sync.RWMutex
	prefix  string
	objects map[string]memoryObject
}

type memoryObject struct {
	data []byte
	info ObjectInfo
}

func NewMemoryStore(prefix string) *MemoryStore {
	normalized, err := normalizePrefix(prefix)
	if err != nil {
		// Retain the invalid value so operations report it instead of silently
		// broadening writes to the bucket root.
		normalized = prefix
	}
	return &MemoryStore{
		prefix:  normalized,
		objects: make(map[string]memoryObject),
	}
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	object, ok := s.objects[physical]
	s.mu.RUnlock()
	if !ok {
		return nil, &NotFoundError{Key: key}
	}
	return append([]byte(nil), object.data...), nil
}

func (s *MemoryStore) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	s.mu.RLock()
	object, ok := s.objects[physical]
	s.mu.RUnlock()
	if !ok {
		return ObjectInfo{}, &NotFoundError{Key: key}
	}
	info := object.info
	info.Key = key
	info.Metadata = cloneMetadata(info.Metadata)
	return info, nil
}

func (s *MemoryStore) Put(ctx context.Context, key string, body io.ReadSeeker, options PutOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	data, digest, err := readAndHash(body)
	if err != nil {
		return fmt.Errorf("read object %q: %w", key, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.objects[physical]; ok {
		if objectMD5(existing.info) == digest {
			return nil
		}
		if options.FailIfExists || options.CreateOnly {
			return &ConflictError{Key: key}
		}
	}

	metadata := cloneMetadata(options.Metadata)
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["md5"] = digest
	s.objects[physical] = memoryObject{
		data: append([]byte(nil), data...),
		info: ObjectInfo{
			Key:                  physical,
			Size:                 int64(len(data)),
			ETag:                 digest,
			Metadata:             metadata,
			ContentType:          options.ContentType,
			CacheControl:         options.CacheControl,
			ServerSideEncryption: options.ServerSideEncryption,
			LastModified:         time.Now().UTC(),
		},
	}
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.objects, physical)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) DeleteIfMatch(ctx context.Context, key, etag string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	object, exists := s.objects[physical]
	if !exists {
		return &NotFoundError{Key: key}
	}
	if trimETag(object.info.ETag) != trimETag(etag) {
		return &ConflictError{Key: key}
	}
	delete(s.objects, physical)
	return nil
}

func (s *MemoryStore) Copy(ctx context.Context, source, destination string, options CopyOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourcePhysical, err := JoinKey(s.prefix, source)
	if err != nil {
		return err
	}
	destinationPhysical, err := JoinKey(s.prefix, destination)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sourceObject, ok := s.objects[sourcePhysical]
	if !ok {
		return &NotFoundError{Key: source}
	}
	if options.SourceETag != "" && trimETag(options.SourceETag) != trimETag(sourceObject.info.ETag) {
		return &ConflictError{Key: source}
	}
	if existing, ok := s.objects[destinationPhysical]; ok {
		if objectMD5(existing.info) == objectMD5(sourceObject.info) {
			return nil
		}
		if options.CreateOnly || options.FailIfExists {
			return &ConflictError{Key: destination}
		}
	}
	cloned := sourceObject
	cloned.data = append([]byte(nil), sourceObject.data...)
	cloned.info.Metadata = cloneMetadata(sourceObject.info.Metadata)
	cloned.info.Key = destinationPhysical
	cloned.info.LastModified = time.Now().UTC()
	s.objects[destinationPhysical] = cloned
	return nil
}

func (s *MemoryStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	physicalPrefix, err := normalizePrefix(s.prefix)
	if err != nil {
		return nil, err
	}
	if prefix != "" {
		physicalPrefix, err = JoinKey(s.prefix, prefix)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(prefix, "/") {
			physicalPrefix += "/"
		}
	}

	s.mu.RLock()
	objects := make([]ObjectInfo, 0)
	for physical, object := range s.objects {
		if !strings.HasPrefix(physical, physicalPrefix) {
			continue
		}
		logical, ok := logicalKey(s.prefix, physical)
		if !ok {
			continue
		}
		info := object.info
		info.Key = logical
		info.Metadata = cloneMetadata(info.Metadata)
		objects = append(objects, info)
	}
	s.mu.RUnlock()
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

func readAndHash(body io.ReadSeeker) ([]byte, string, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	hash := md5.New()
	data, err := io.ReadAll(io.TeeReader(body, hash))
	if err != nil {
		return nil, "", err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	return data, hex.EncodeToString(hash.Sum(nil)), nil
}

func trimETag(etag string) string {
	return strings.Trim(strings.TrimSpace(etag), `"`)
}

func objectMD5(info ObjectInfo) string {
	if digest := info.Metadata["md5"]; digest != "" {
		return strings.ToLower(digest)
	}
	return strings.ToLower(trimETag(info.ETag))
}
