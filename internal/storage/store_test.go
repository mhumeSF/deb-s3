package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestJoinKey(t *testing.T) {
	tests := []struct {
		prefix  string
		key     string
		want    string
		wantErr bool
	}{
		{prefix: "", key: "dists/stable/Release", want: "dists/stable/Release"},
		{prefix: "/repos/team/", key: "/dists/stable/Release", want: "repos/team/dists/stable/Release"},
		{prefix: "repo", key: "../secret", wantErr: true},
		{prefix: "../repo", key: "dists/stable", wantErr: true},
		{prefix: "repo", key: `dists\stable`, wantErr: true},
		{prefix: "repo", key: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := JoinKey(tt.prefix, tt.key)
		if (err != nil) != tt.wantErr {
			t.Errorf("JoinKey(%q, %q) error = %v, wantErr %v", tt.prefix, tt.key, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("JoinKey(%q, %q) = %q, want %q", tt.prefix, tt.key, got, tt.want)
		}
	}
}

func TestMemoryStoreRejectsInvalidConfiguredPrefix(t *testing.T) {
	store := NewMemoryStore("../outside")
	if err := store.Put(context.Background(), "key", bytes.NewReader(nil), PutOptions{}); err == nil {
		t.Fatal("Put with invalid configured prefix succeeded")
	}
}

func TestMemoryStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("repositories/demo")
	key := "dists/stable/Release"
	content := []byte("release one")
	options := PutOptions{
		ContentType:          "text/plain; charset=utf-8",
		CacheControl:         "max-age=60",
		Metadata:             map[string]string{"source": "test"},
		ServerSideEncryption: true,
	}
	if err := store.Put(ctx, key, bytes.NewReader(content), options); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("Get() = %q, want %q", got, content)
	}
	got[0] = 'X'
	again, _ := store.Get(ctx, key)
	if !bytes.Equal(again, content) {
		t.Fatal("Get returned storage-owned bytes")
	}

	info, err := store.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Key != key || info.Size != int64(len(content)) {
		t.Fatalf("Head() = %#v", info)
	}
	if info.ContentType != options.ContentType || info.CacheControl != options.CacheControl {
		t.Fatalf("Head headers = %#v", info)
	}
	if info.Metadata["source"] != "test" || info.Metadata["md5"] == "" {
		t.Fatalf("Head metadata = %#v", info.Metadata)
	}
	if !info.ServerSideEncryption {
		t.Fatal("Head did not retain encryption setting")
	}

	objects, err := store.List(ctx, "dists/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != key {
		t.Fatalf("List() = %#v", objects)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete() = %v", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreExistingObjectRules(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("")
	key := "Packages"
	if err := store.Put(ctx, key, bytes.NewReader([]byte("one")), PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// Identical bytes are always idempotent, including create-only writes.
	if err := store.Put(ctx, key, bytes.NewReader([]byte("one")), PutOptions{CreateOnly: true}); err != nil {
		t.Fatalf("identical create-only Put() = %v", err)
	}
	if err := store.Put(ctx, key, bytes.NewReader([]byte("two")), PutOptions{FailIfExists: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Put() error = %v, want ErrConflict", err)
	}
	if err := store.Put(ctx, key, bytes.NewReader([]byte("two")), PutOptions{}); err != nil {
		t.Fatalf("overwriting Put() = %v", err)
	}
	got, _ := store.Get(ctx, key)
	if string(got) != "two" {
		t.Fatalf("overwritten object = %q, want two", got)
	}
}

func TestMemoryStoreConditionalDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("prefix")
	if err := store.Put(ctx, "lock", bytes.NewReader([]byte("owner")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := store.Head(ctx, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIfMatch(ctx, "lock", "different"); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched DeleteIfMatch() error = %v", err)
	}
	if _, err := store.Get(ctx, "lock"); err != nil {
		t.Fatalf("mismatched conditional delete removed object: %v", err)
	}
	if err := store.DeleteIfMatch(ctx, "lock", info.ETag); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "lock"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("matched conditional delete retained object: %v", err)
	}
}

func TestMemoryStoreCopy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("prefix")
	if err := store.Put(ctx, "source", bytes.NewReader([]byte("contents")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	source, _ := store.Head(ctx, "source")
	if err := store.Copy(ctx, "source", "destination", CopyOptions{SourceETag: source.ETag, CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, "destination")
	if string(got) != "contents" {
		t.Fatalf("copied object = %q", got)
	}
	if err := store.Copy(ctx, "source", "other", CopyOptions{SourceETag: "wrong"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Copy with wrong ETag error = %v, want ErrConflict", err)
	}
	if err := store.Copy(ctx, "missing", "other", CopyOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Copy missing error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryStore("")
	if err := store.Put(ctx, "key", bytes.NewReader(nil), PutOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
}

func TestObjectMD5UsesMetadataThenETagFallback(t *testing.T) {
	if got := objectMD5(ObjectInfo{ETag: `"etag-digest"`}); got != "etag-digest" {
		t.Fatalf("ETag fallback = %q", got)
	}
	if got := objectMD5(ObjectInfo{
		ETag:     `"etag-digest"`,
		Metadata: map[string]string{"md5": "metadata-digest"},
	}); got != "metadata-digest" {
		t.Fatalf("metadata digest = %q", got)
	}
}
