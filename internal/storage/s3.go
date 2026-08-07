package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	appconfig "github.com/mhumesf/deb-s3-go/internal/config"
)

type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type S3Store struct {
	client     s3API
	bucket     string
	prefix     string
	acl        types.ObjectCannedACL
	encryption bool
}

func NewS3Store(ctx context.Context, cfg appconfig.Config) (*S3Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	prefix, err := normalizePrefix(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	if cfg.Endpoint != "" {
		if err := validateEndpoint(cfg.Endpoint); err != nil {
			return nil, err
		}
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.S3Region),
	}
	if cfg.AccessKeyID != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}
	if cfg.ChecksumWhenRequired {
		loadOptions = append(loadOptions, awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired))
	}
	if cfg.ProxyURI != "" {
		httpClient, err := proxyHTTPClient(cfg.ProxyURI)
		if err != nil {
			return nil, err
		}
		loadOptions = append(loadOptions, awsconfig.WithHTTPClient(httpClient))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.ForcePathStyle
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})

	acl, err := visibilityACL(cfg.Visibility)
	if err != nil {
		return nil, err
	}
	return newS3Store(client, cfg.Bucket, prefix, acl, cfg.Encryption), nil
}

func newS3Store(client s3API, bucket, prefix string, acl types.ObjectCannedACL, encryption bool) *S3Store {
	return &S3Store{
		client:     client,
		bucket:     bucket,
		prefix:     strings.Trim(prefix, "/"),
		acl:        acl,
		encryption: encryption,
	}
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(physical),
	})
	if err != nil {
		return nil, mapS3Error(key, err)
	}
	defer output.Body.Close()
	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	return data, nil
}

func (s *S3Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(physical),
	})
	if err != nil {
		return ObjectInfo{}, mapS3Error(key, err)
	}
	return ObjectInfo{
		Key:                  key,
		Size:                 aws.ToInt64(output.ContentLength),
		ETag:                 trimETag(aws.ToString(output.ETag)),
		Metadata:             cloneMetadata(output.Metadata),
		ContentType:          aws.ToString(output.ContentType),
		CacheControl:         aws.ToString(output.CacheControl),
		ServerSideEncryption: output.ServerSideEncryption == types.ServerSideEncryptionAes256,
		LastModified:         aws.ToTime(output.LastModified),
	}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, body io.ReadSeeker, options PutOptions) error {
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	digest, size, err := hashSeeker(body)
	if err != nil {
		return fmt.Errorf("hash object %q: %w", key, err)
	}

	existing, err := s.Head(ctx, key)
	if err == nil {
		if objectMD5(existing) == digest {
			return nil
		}
		if options.FailIfExists || options.CreateOnly {
			return &ConflictError{Key: key}
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	metadata := cloneMetadata(options.Metadata)
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["md5"] = digest
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(physical),
		Body:          body,
		ContentLength: aws.Int64(size),
		Metadata:      metadata,
	}
	if s.acl != "" {
		input.ACL = s.acl
	}
	if options.ContentType != "" {
		input.ContentType = aws.String(options.ContentType)
	}
	if options.CacheControl != "" {
		input.CacheControl = aws.String(options.CacheControl)
	}
	if s.encryption || options.ServerSideEncryption {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	}
	if options.CreateOnly || (options.FailIfExists && errors.Is(err, ErrNotFound)) {
		input.IfNoneMatch = aws.String("*")
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return mapS3Error(key, err)
	}
	return nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(physical),
	})
	if err != nil {
		return mapS3Error(key, err)
	}
	return nil
}

func (s *S3Store) DeleteIfMatch(ctx context.Context, key, etag string) error {
	physical, err := JoinKey(s.prefix, key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(physical),
		IfMatch: aws.String(trimETag(etag)),
	})
	if err != nil {
		return mapS3Error(key, err)
	}
	return nil
}

func (s *S3Store) Copy(ctx context.Context, source, destination string, options CopyOptions) error {
	sourcePhysical, err := JoinKey(s.prefix, source)
	if err != nil {
		return err
	}
	destinationPhysical, err := JoinKey(s.prefix, destination)
	if err != nil {
		return err
	}
	sourceInfo, err := s.Head(ctx, source)
	if err != nil {
		return err
	}
	if options.SourceETag != "" && trimETag(options.SourceETag) != trimETag(sourceInfo.ETag) {
		return &ConflictError{Key: source}
	}
	destinationInfo, destinationErr := s.Head(ctx, destination)
	if destinationErr == nil {
		if sourceDigest, destinationDigest := objectMD5(sourceInfo), objectMD5(destinationInfo); sourceDigest != "" && sourceDigest == destinationDigest {
			return nil
		}
		if options.CreateOnly || options.FailIfExists {
			return &ConflictError{Key: destination}
		}
	} else if !errors.Is(destinationErr, ErrNotFound) {
		return destinationErr
	}

	input := &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(destinationPhysical),
		CopySource: aws.String(url.PathEscape(s.bucket + "/" + sourcePhysical)),
	}
	if s.acl != "" {
		input.ACL = s.acl
	}
	if s.encryption {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	}
	if options.SourceETag != "" {
		input.CopySourceIfMatch = aws.String(trimETag(options.SourceETag))
	}
	if (options.CreateOnly || options.FailIfExists) && errors.Is(destinationErr, ErrNotFound) {
		input.IfNoneMatch = aws.String("*")
	}
	if _, err := s.client.CopyObject(ctx, input); err != nil {
		return mapS3Error(destination, err)
	}
	return nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
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
	} else if physicalPrefix != "" {
		physicalPrefix += "/"
	}

	var objects []ObjectInfo
	var continuationToken *string
	for {
		output, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(physicalPrefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list objects under %q: %w", prefix, err)
		}
		for _, object := range output.Contents {
			logical, ok := logicalKey(s.prefix, aws.ToString(object.Key))
			if !ok {
				continue
			}
			objects = append(objects, ObjectInfo{
				Key:          logical,
				Size:         aws.ToInt64(object.Size),
				ETag:         trimETag(aws.ToString(object.ETag)),
				LastModified: aws.ToTime(object.LastModified),
			})
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		if output.NextContinuationToken == nil || *output.NextContinuationToken == "" {
			return nil, errors.New("S3 returned a truncated object listing without a continuation token")
		}
		continuationToken = output.NextContinuationToken
	}
	return objects, nil
}

func hashSeeker(body io.ReadSeeker) (string, int64, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := md5.New()
	size, err := io.Copy(hash, body)
	if err != nil {
		return "", 0, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func visibilityACL(visibility string) (types.ObjectCannedACL, error) {
	switch visibility {
	case "public":
		return types.ObjectCannedACLPublicRead, nil
	case "private":
		return types.ObjectCannedACLPrivate, nil
	case "authenticated":
		return types.ObjectCannedACLAuthenticatedRead, nil
	case "bucket_owner":
		return types.ObjectCannedACLBucketOwnerFullControl, nil
	case "nil":
		return "", nil
	default:
		return "", fmt.Errorf("invalid visibility %q", visibility)
	}
}

func proxyHTTPClient(proxyURI string) (*http.Client, error) {
	parsed, err := url.Parse(proxyURI)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy URI %q", proxyURI)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport has unexpected type")
	}
	cloned := transport.Clone()
	cloned.Proxy = http.ProxyURL(parsed)
	return &http.Client{Transport: cloned}, nil
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid S3 endpoint %q", endpoint)
	}
	return nil
}

func mapS3Error(key string, err error) error {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return &NotFoundError{Key: key}
		case "PreconditionFailed", "ConditionalRequestConflict", "412", "409":
			return &ConflictError{Key: key}
		}
	}
	return fmt.Errorf("S3 object %q: %w", key, err)
}
