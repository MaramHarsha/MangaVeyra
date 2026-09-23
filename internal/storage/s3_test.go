package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

// TestS3Store runs against a real S3-compatible server when MV_TEST_S3_ENDPOINT is set, e.g.
//
//	docker run -d -p 9000:9000 -e MINIO_ROOT_USER=mvadmin -e MINIO_ROOT_PASSWORD=mvsecret123 quay.io/minio/minio server /data
//	MV_TEST_S3_ENDPOINT=127.0.0.1:9000 MV_TEST_S3_ACCESS_KEY=mvadmin MV_TEST_S3_SECRET_KEY=mvsecret123 go test ./internal/storage/
func TestS3Store(t *testing.T) {
	endpoint := os.Getenv("MV_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MV_TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	s, err := NewS3(ctx, S3Options{Endpoint: endpoint, Region: "us-east-1", Bucket: "mangaveyra-test", AccessKey: os.Getenv("MV_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("MV_TEST_S3_SECRET_KEY"), PathStyle: true, Prefix: "ci", CreateBucket: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	key := NewKey("projects/p1", "image/png")
	if err := PutBytes(ctx, s, key, pngBytes(t, 8, 8), "image/png"); err != nil {
		t.Fatal(err)
	}
	obj, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj)
	obj.Close()
	if info.ContentType != "image/png" || int64(len(b)) != info.Size || DetectContentType(b) != "image/png" {
		t.Fatalf("get: %+v", info)
	}
	list, err := s.List(ctx, "projects/p1/")
	if err != nil || len(list) != 1 || list[0].Key != key {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}
