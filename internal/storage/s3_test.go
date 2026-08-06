package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	appconfig "github.com/mhumesf/deb-s3/internal/config"
)

type fakeS3 struct {
	getObject    func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error)
	headObject   func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
	putObject    func(context.Context, *s3.PutObjectInput) (*s3.PutObjectOutput, error)
	deleteObject func(context.Context, *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
	copyObject   func(context.Context, *s3.CopyObjectInput) (*s3.CopyObjectOutput, error)
	listObjects  func(context.Context, *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
}

func (f *fakeS3) GetObject(ctx context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getObject(ctx, input)
}

func (f *fakeS3) HeadObject(ctx context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return f.headObject(ctx, input)
}

func (f *fakeS3) PutObject(ctx context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return f.putObject(ctx, input)
}

func (f *fakeS3) DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return f.deleteObject(ctx, input)
}

func (f *fakeS3) CopyObject(ctx context.Context, input *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	return f.copyObject(ctx, input)
}

func (f *fakeS3) ListObjectsV2(ctx context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return f.listObjects(ctx, input)
}

func TestS3PutConstructsRequest(t *testing.T) {
	var captured *s3.PutObjectInput
	client := &fakeS3{
		headObject: func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
		},
		putObject: func(_ context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			captured = input
			return &s3.PutObjectOutput{}, nil
		},
	}
	store := newS3Store(client, "bucket", "repo/root", types.ObjectCannedACLPublicRead, true)
	body := bytes.NewReader([]byte("manifest"))
	err := store.Put(context.Background(), "dists/stable/Release", body, PutOptions{
		ContentType:  "text/plain; charset=utf-8",
		CacheControl: "max-age=60",
		Metadata:     map[string]string{"custom": "value"},
		CreateOnly:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(captured.Bucket) != "bucket" || aws.ToString(captured.Key) != "repo/root/dists/stable/Release" {
		t.Fatalf("Put destination = %q/%q", aws.ToString(captured.Bucket), aws.ToString(captured.Key))
	}
	if captured.ACL != types.ObjectCannedACLPublicRead {
		t.Fatalf("ACL = %q", captured.ACL)
	}
	if captured.ServerSideEncryption != types.ServerSideEncryptionAes256 {
		t.Fatalf("encryption = %q", captured.ServerSideEncryption)
	}
	if aws.ToString(captured.ContentType) != "text/plain; charset=utf-8" || aws.ToString(captured.CacheControl) != "max-age=60" {
		t.Fatalf("headers: content-type=%q cache-control=%q", aws.ToString(captured.ContentType), aws.ToString(captured.CacheControl))
	}
	wantMD5 := fmt.Sprintf("%x", md5.Sum([]byte("manifest")))
	if captured.Metadata["custom"] != "value" || captured.Metadata["md5"] != wantMD5 {
		t.Fatalf("metadata = %#v, want md5=%q", captured.Metadata, wantMD5)
	}
	if aws.ToString(captured.IfNoneMatch) != "*" {
		t.Fatalf("IfNoneMatch = %q", aws.ToString(captured.IfNoneMatch))
	}
	data, err := io.ReadAll(captured.Body)
	if err != nil || string(data) != "manifest" {
		t.Fatalf("body = %q, error=%v", data, err)
	}
}

func TestS3PutSkipsIdenticalAndRejectsConflict(t *testing.T) {
	putCalls := 0
	digest := fmt.Sprintf("%x", md5.Sum([]byte("two")))
	client := &fakeS3{
		headObject: func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{Metadata: map[string]string{"md5": digest}}, nil
		},
		putObject: func(context.Context, *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			putCalls++
			return &s3.PutObjectOutput{}, nil
		},
	}
	store := newS3Store(client, "bucket", "", "", false)
	if err := store.Put(context.Background(), "key", bytes.NewReader([]byte("two")), PutOptions{FailIfExists: true}); err != nil {
		t.Fatalf("identical Put() = %v", err)
	}
	if putCalls != 0 {
		t.Fatalf("identical Put called S3 %d times", putCalls)
	}
	if err := store.Put(context.Background(), "key", bytes.NewReader([]byte("different")), PutOptions{FailIfExists: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Put() error = %v, want ErrConflict", err)
	}
}

func TestS3PutOverwritesWithoutACLWhenAllowed(t *testing.T) {
	var captured *s3.PutObjectInput
	client := &fakeS3{
		headObject: func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ETag: aws.String(`"different"`)}, nil
		},
		putObject: func(_ context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			captured = input
			return &s3.PutObjectOutput{}, nil
		},
	}
	store := newS3Store(client, "bucket", "", "", false)
	if err := store.Put(context.Background(), "key", bytes.NewReader([]byte("new")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if captured.ACL != "" {
		t.Fatalf("ACL = %q, want absent", captured.ACL)
	}
	if captured.IfNoneMatch != nil {
		t.Fatalf("IfNoneMatch = %q, want absent for overwrite", aws.ToString(captured.IfNoneMatch))
	}
}

func TestS3GetHeadDeleteAndCopyRequests(t *testing.T) {
	modified := time.Now().UTC().Truncate(time.Second)
	client := &fakeS3{
		getObject: func(_ context.Context, input *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			if aws.ToString(input.Key) != "prefix/source" {
				t.Fatalf("Get key = %q", aws.ToString(input.Key))
			}
			return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("data")))}, nil
		},
		headObject: func(_ context.Context, input *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			if aws.ToString(input.Key) == "prefix/destination" {
				return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
			}
			return &s3.HeadObjectOutput{
				ContentLength:        aws.Int64(4),
				ETag:                 aws.String(`"etag"`),
				Metadata:             map[string]string{"md5": "digest"},
				ContentType:          aws.String("text/plain"),
				CacheControl:         aws.String("no-cache"),
				LastModified:         aws.Time(modified),
				ServerSideEncryption: types.ServerSideEncryptionAes256,
			}, nil
		},
		deleteObject: func(_ context.Context, input *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
			if aws.ToString(input.Key) != "prefix/source" {
				t.Fatalf("Delete key = %q", aws.ToString(input.Key))
			}
			return &s3.DeleteObjectOutput{}, nil
		},
		copyObject: func(_ context.Context, input *s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
			if aws.ToString(input.Key) != "prefix/destination" {
				t.Fatalf("Copy key = %q", aws.ToString(input.Key))
			}
			if aws.ToString(input.CopySourceIfMatch) != "etag" || aws.ToString(input.IfNoneMatch) != "*" {
				t.Fatalf("Copy conditions: source=%q destination=%q", aws.ToString(input.CopySourceIfMatch), aws.ToString(input.IfNoneMatch))
			}
			if aws.ToString(input.CopySource) != "bucket%2Fprefix%2Fsource" {
				t.Fatalf("CopySource = %q", aws.ToString(input.CopySource))
			}
			return &s3.CopyObjectOutput{}, nil
		},
	}
	store := newS3Store(client, "bucket", "prefix", types.ObjectCannedACLPrivate, true)
	data, err := store.Get(context.Background(), "source")
	if err != nil || string(data) != "data" {
		t.Fatalf("Get() = %q, %v", data, err)
	}
	info, err := store.Head(context.Background(), "source")
	if err != nil {
		t.Fatal(err)
	}
	if info.Key != "source" || info.ETag != "etag" || info.Size != 4 || !info.ServerSideEncryption || !info.LastModified.Equal(modified) {
		t.Fatalf("Head() = %#v", info)
	}
	if err := store.Copy(context.Background(), "source", "destination", CopyOptions{SourceETag: `"etag"`, CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "source"); err != nil {
		t.Fatal(err)
	}
}

func TestS3ListPaginatesAndRemovesStoragePrefix(t *testing.T) {
	page := 0
	client := &fakeS3{
		listObjects: func(_ context.Context, input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			page++
			if aws.ToString(input.Prefix) != "repo/dists/" {
				t.Fatalf("List prefix = %q", aws.ToString(input.Prefix))
			}
			if page == 1 {
				objects := make([]types.Object, 1000)
				for i := range objects {
					objects[i] = types.Object{Key: aws.String(fmt.Sprintf("repo/dists/object-%04d", i)), Size: aws.Int64(int64(i))}
				}
				return &s3.ListObjectsV2Output{
					Contents:              objects,
					IsTruncated:           aws.Bool(true),
					NextContinuationToken: aws.String("next-page"),
				}, nil
			}
			if aws.ToString(input.ContinuationToken) != "next-page" {
				t.Fatalf("continuation token = %q", aws.ToString(input.ContinuationToken))
			}
			return &s3.ListObjectsV2Output{
				Contents:    []types.Object{{Key: aws.String("repo/dists/object-1000"), Size: aws.Int64(1000)}},
				IsTruncated: aws.Bool(false),
			}, nil
		},
	}
	store := newS3Store(client, "bucket", "repo", "", false)
	objects, err := store.List(context.Background(), "dists/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1001 || objects[0].Key != "dists/object-0000" || objects[1000].Key != "dists/object-1000" {
		t.Fatalf("List returned %d objects, first=%q last=%q", len(objects), objects[0].Key, objects[len(objects)-1].Key)
	}
}

func TestS3ConditionalDeleteUsesIfMatch(t *testing.T) {
	client := &fakeS3{deleteObject: func(_ context.Context, input *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
		if aws.ToString(input.Bucket) != "bucket" || aws.ToString(input.Key) != "prefix/dists/stable/lockfile" {
			t.Fatalf("DeleteIfMatch destination = %q/%q", aws.ToString(input.Bucket), aws.ToString(input.Key))
		}
		if aws.ToString(input.IfMatch) != "etag" {
			t.Fatalf("DeleteIfMatch If-Match = %q", aws.ToString(input.IfMatch))
		}
		return &s3.DeleteObjectOutput{}, nil
	}}
	store := newS3Store(client, "bucket", "prefix", "", false)
	if err := store.DeleteIfMatch(context.Background(), "dists/stable/lockfile", `"etag"`); err != nil {
		t.Fatal(err)
	}
}

func TestS3MapsServiceErrors(t *testing.T) {
	for code, target := range map[string]error{
		"NoSuchKey":                  ErrNotFound,
		"NotFound":                   ErrNotFound,
		"PreconditionFailed":         ErrConflict,
		"ConditionalRequestConflict": ErrConflict,
	} {
		err := mapS3Error("key", &smithy.GenericAPIError{Code: code, Message: "test"})
		if !errors.Is(err, target) {
			t.Errorf("mapS3Error(%s) = %v, want %v", code, err, target)
		}
	}
}

func TestS3ConfigurationMapping(t *testing.T) {
	cfg := appconfig.New()
	cfg.Bucket = "bucket"
	cfg.AccessKeyID = "key"
	cfg.SecretAccessKey = "secret"
	cfg.SessionToken = "token"
	cfg.Endpoint = "https://objects.example.test"
	cfg.ForcePathStyle = true
	cfg.ChecksumWhenRequired = true
	cfg.Visibility = "nil"

	store, err := NewS3Store(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := store.client.(*s3.Client)
	if !ok {
		t.Fatalf("client type = %T", store.client)
	}
	options := client.Options()
	if aws.ToString(options.BaseEndpoint) != cfg.Endpoint || !options.UsePathStyle {
		t.Fatalf("client endpoint=%q path-style=%v", aws.ToString(options.BaseEndpoint), options.UsePathStyle)
	}
	if options.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired {
		t.Fatalf("checksum calculation = %v", options.RequestChecksumCalculation)
	}
	if store.acl != "" {
		t.Fatalf("nil visibility ACL = %q", store.acl)
	}
	credentials, err := options.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "key" || credentials.SecretAccessKey != "secret" || credentials.SessionToken != "token" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestS3AWSStyleConfigurationMapping(t *testing.T) {
	cfg := appconfig.New()
	cfg.Bucket = "bucket"
	cfg.AccessKeyID = "key"
	cfg.SecretAccessKey = "secret"

	store, err := NewS3Store(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := store.client.(*s3.Client)
	if !ok {
		t.Fatalf("client type = %T", store.client)
	}
	options := client.Options()
	if options.BaseEndpoint != nil || options.UsePathStyle {
		t.Fatalf("AWS-style client endpoint=%q path-style=%v", aws.ToString(options.BaseEndpoint), options.UsePathStyle)
	}
}

func TestS3ConfigurationRejectsInvalidURLsAndPrefix(t *testing.T) {
	for name, change := range map[string]func(*appconfig.Config){
		"endpoint": func(cfg *appconfig.Config) { cfg.Endpoint = "not-a-url" },
		"proxy":    func(cfg *appconfig.Config) { cfg.ProxyURI = "ftp://proxy.example.test" },
		"prefix":   func(cfg *appconfig.Config) { cfg.Prefix = "../outside" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := appconfig.New()
			cfg.Bucket = "bucket"
			cfg.AccessKeyID = "key"
			cfg.SecretAccessKey = "secret"
			change(&cfg)
			if _, err := NewS3Store(context.Background(), cfg); err == nil {
				t.Fatal("NewS3Store succeeded")
			}
		})
	}
}

func TestVisibilityACL(t *testing.T) {
	want := map[string]types.ObjectCannedACL{
		"public":        types.ObjectCannedACLPublicRead,
		"private":       types.ObjectCannedACLPrivate,
		"authenticated": types.ObjectCannedACLAuthenticatedRead,
		"bucket_owner":  types.ObjectCannedACLBucketOwnerFullControl,
		"nil":           "",
	}
	for visibility, expected := range want {
		actual, err := visibilityACL(visibility)
		if err != nil || actual != expected {
			t.Errorf("visibilityACL(%q) = %q, %v; want %q", visibility, actual, err, expected)
		}
	}
}

func TestProxyHTTPClient(t *testing.T) {
	client, err := proxyHTTPClient("http://proxy.example.test:8080")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://s3.example.test", nil)
	proxy, err := transport.Proxy(request)
	if err != nil || proxy.String() != "http://proxy.example.test:8080" {
		t.Fatalf("proxy = %v, error=%v", proxy, err)
	}
	if _, err := proxyHTTPClient("not a URI"); err == nil {
		t.Fatal("invalid proxy URI succeeded")
	}
}
