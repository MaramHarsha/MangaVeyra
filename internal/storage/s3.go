package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Options configures an S3-compatible store.
type S3Options struct {
	Endpoint  string // host[:port], no scheme
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	PathStyle bool
	Prefix    string // optional key prefix inside the bucket
	// CreateBucket creates the bucket on startup when it does not exist.
	CreateBucket bool
}

// S3 stores objects in an S3-compatible bucket (AWS S3, MinIO, R2, B2, Garage...).
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3 connects to the bucket and verifies it exists (creating it when asked).
func NewS3(ctx context.Context, o S3Options) (*S3, error) {
	lookup := minio.BucketLookupAuto
	if o.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(o.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure:       o.UseSSL,
		Region:       o.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 client: %w", err)
	}
	ok, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("storage: s3 bucket %q: %w", o.Bucket, err)
	}
	if !ok {
		if !o.CreateBucket {
			return nil, fmt.Errorf("storage: s3 bucket %q does not exist", o.Bucket)
		}
		if err := client.MakeBucket(ctx, o.Bucket, minio.MakeBucketOptions{Region: o.Region}); err != nil {
			return nil, fmt.Errorf("storage: create bucket %q: %w", o.Bucket, err)
		}
	}
	prefix := strings.Trim(o.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: client, bucket: o.Bucket, prefix: prefix}, nil
}

// Kind implements Store.
func (s *S3) Kind() string { return "s3" }

func (s *S3) object(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

// Put implements Store.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	name, err := s.object(key)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = ContentTypeFor(key)
	}
	_, err = s.client.PutObject(ctx, s.bucket, name, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: s3 put %s: %w", key, err)
	}
	return nil
}

// Get implements Store.
func (s *S3) Get(ctx context.Context, key string) (Object, Info, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, Info{}, err
	}
	name, _ := s.object(key)
	obj, err := s.client.GetObject(ctx, s.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return nil, Info{}, mapS3Err(err)
	}
	return obj, info, nil
}

// Stat implements Store.
func (s *S3) Stat(ctx context.Context, key string) (Info, error) {
	name, err := s.object(key)
	if err != nil {
		return Info{}, err
	}
	oi, err := s.client.StatObject(ctx, s.bucket, name, minio.StatObjectOptions{})
	if err != nil {
		return Info{}, mapS3Err(err)
	}
	ct := oi.ContentType
	if ct == "" || ct == "binary/octet-stream" {
		ct = ContentTypeFor(key)
	}
	return Info{Key: key, Size: oi.Size, ContentType: ct, ModTime: oi.LastModified.UTC(), ETag: `"` + oi.ETag + `"`}, nil
}

// Delete implements Store.
func (s *S3) Delete(ctx context.Context, key string) error {
	name, err := s.object(key)
	if err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, name, minio.RemoveObjectOptions{}); err != nil {
		if errors.Is(mapS3Err(err), ErrNotFound) {
			return nil
		}
		return fmt.Errorf("storage: s3 delete %s: %w", key, err)
	}
	return nil
}

// List implements Store.
func (s *S3) List(ctx context.Context, prefix string) ([]Info, error) {
	var out []Info
	for oi := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + prefix, Recursive: true}) {
		if oi.Err != nil {
			return nil, fmt.Errorf("storage: s3 list: %w", oi.Err)
		}
		key := strings.TrimPrefix(oi.Key, s.prefix)
		out = append(out, Info{Key: key, Size: oi.Size, ContentType: ContentTypeFor(key), ModTime: oi.LastModified.UTC(), ETag: `"` + oi.ETag + `"`})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Ping checks the bucket is reachable (used by /readyz).
func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}

func mapS3Err(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
		return ErrNotFound
	}
	return err
}
