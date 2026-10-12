package main

// This test is copied into the entrypoint module of a server whose APIs
// list a bucket, beside the buckets.go the build wrote, by
// TestBucketsReachTheEmulator, which runs fake-gcs-server and sets the
// derived variables of a bucket connection to it in the environment (D54).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/bucket"
	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
)

// TestBucketOnTheEmulator: the derived variables of a bucket connection to
// the emulator open a bucket that puts, gets, lists a page at a time and
// deletes objects, and signs URLs the emulator takes a PUT and a GET at.
func TestBucketOnTheEmulator(t *testing.T) {
	ctx := context.Background()
	conn, err := stackconfig.LoadBucket("SHOP_MEDIA_BUCKET")
	if err != nil {
		t.Fatal(err)
	}
	created, err := http.Post(conn.Endpoint+"/storage/v1/b?project=test", "application/json", strings.NewReader(`{"name":"`+conn.Name+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	if created.StatusCode != http.StatusOK && created.StatusCode != http.StatusConflict {
		t.Fatalf("create the bucket: %s", created.Status)
	}
	b, err := openBucket(ctx, "SHOP_MEDIA_BUCKET", conn)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBuckets()
	if b.Name() != conn.Name {
		t.Errorf("Name = %s, want %s", b.Name(), conn.Name)
	}
	prefix := "go-" + time.Now().Format("150405.000000") + "/"

	stored, err := b.Put(ctx, prefix+"a.txt", strings.NewReader("streamed bytes"), bucket.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != prefix+"a.txt" || stored.Size != 14 || stored.ContentType != "text/plain" {
		t.Errorf("Put = %+v", stored)
	}
	for _, name := range []string{"b.txt", "c.txt"} {
		if _, err := b.Put(ctx, prefix+name, bytes.NewReader([]byte(name)), bucket.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	r, err := b.Get(ctx, prefix+"a.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(data) != "streamed bytes" || r.Object.ContentType != "text/plain" {
		t.Errorf("Get = %q (%+v), %v", data, r.Object, err)
	}

	first, err := b.List(ctx, bucket.ListOptions{Prefix: prefix, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 2 || first.Objects[0].Name != prefix+"a.txt" || first.NextPageToken == "" {
		t.Fatalf("the first page = %+v", first)
	}
	second, err := b.List(ctx, bucket.ListOptions{Prefix: prefix, PageSize: 2, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 1 || second.Objects[0].Name != prefix+"c.txt" || second.NextPageToken != "" {
		t.Errorf("the second page = %+v", second)
	}

	upload, err := b.SignedURL(ctx, prefix+"upload.png", bucket.SignedURLOptions{Method: bucket.MethodPut, Expires: 5 * time.Minute, ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(upload, conn.Endpoint+"/"+conn.Name+"/") || !strings.Contains(upload, "X-Goog-Algorithm=GOOG4-RSA-SHA256") {
		t.Errorf("the upload URL is %s, not a V4 URL of the emulator's object path", upload)
	}
	put, err := http.NewRequest(http.MethodPut, upload, strings.NewReader("png bytes"))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Type", "image/png")
	if resp, err := http.DefaultClient.Do(put); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT through the signed URL: %v %v", resp, err)
	}
	download, err := b.SignedURL(ctx, prefix+"upload.png", bucket.SignedURLOptions{Method: bucket.MethodGet, Expires: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(download)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "png bytes" {
		t.Errorf("GET through the signed URL: %s %q", resp.Status, body)
	}
	if _, err := b.SignedURL(ctx, prefix+"x", bucket.SignedURLOptions{Method: "DELETE", Expires: time.Minute}); err == nil {
		t.Error("SignedURL signed a DELETE")
	}

	for _, name := range []string{"a.txt", "b.txt", "c.txt", "upload.png"} {
		if err := b.Delete(ctx, prefix+name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Get(ctx, prefix+"a.txt"); !errors.Is(err, bucket.ErrNotFound) {
		t.Errorf("Get of a deleted object = %v, want ErrNotFound", err)
	}
	if err := b.Delete(ctx, prefix+"a.txt"); !errors.Is(err, bucket.ErrNotFound) {
		t.Errorf("Delete of a deleted object = %v, want ErrNotFound", err)
	}
}
