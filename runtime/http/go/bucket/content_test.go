package bucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memBucket is a Bucket kept in memory: Put with IfAbsent checks and
// writes under one lock, as a provider does in one step.
type memBucket struct {
	mu      sync.Mutex
	objects map[string]memObject
	puts    int
	heads   int
}

type memObject struct {
	data        []byte
	contentType string
}

func newMemBucket() *memBucket { return &memBucket{objects: map[string]memObject{}} }

func (b *memBucket) Name() string { return "mem" }

func (b *memBucket) object(name string) Object {
	o := b.objects[name]
	return Object{Name: name, Size: int64(len(o.data)), ContentType: o.contentType}
}

func (b *memBucket) Put(_ context.Context, name string, body io.Reader, opts PutOptions) (PutResult, error) {
	if err := CheckName(name); err != nil {
		return PutResult{}, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return PutResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	if _, held := b.objects[name]; held && opts.IfAbsent {
		return PutResult{Object: b.object(name)}, nil
	}
	b.objects[name] = memObject{data: data, contentType: opts.ContentType}
	return PutResult{Object: b.object(name), Created: true}, nil
}

func (b *memBucket) Head(_ context.Context, name string) (Object, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.heads++
	if _, held := b.objects[name]; !held {
		return Object{}, false, nil
	}
	return b.object(name), true, nil
}

func (b *memBucket) Get(_ context.Context, name string) (*Reader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, held := b.objects[name]
	if !held {
		return nil, fmt.Errorf("bucket mem: get %s: %w", name, ErrNotFound)
	}
	return &Reader{ReadCloser: io.NopCloser(bytes.NewReader(o.data)), Object: b.object(name)}, nil
}

func (b *memBucket) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, held := b.objects[name]; !held {
		return fmt.Errorf("bucket mem: delete %s: %w", name, ErrNotFound)
	}
	delete(b.objects, name)
	return nil
}

func (b *memBucket) List(context.Context, ListOptions) (Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.objects))
	for name := range b.objects {
		names = append(names, name)
	}
	sort.Strings(names)
	page := Page{}
	for _, name := range names {
		page.Objects = append(page.Objects, b.object(name))
	}
	return page, nil
}

func (b *memBucket) SignedURL(_ context.Context, name string, opts SignedURLOptions) (string, error) {
	if err := opts.Check(); err != nil {
		return "", err
	}
	return fmt.Sprintf("https://mem.example/%s?method=%s&expires=%d", name, opts.Method, int(opts.Expires.Seconds())), nil
}

// contentParity is runtime/http/testdata/content_bucket_parity.json.
type contentParity struct {
	MaxBytes int64
	Puts     []struct {
		Name     string
		Text     string
		Bytes    []int
		Hash     string
		Size     int64
		Created  bool
		Object   string
		TooLarge bool
	}
	Hashes struct {
		Valid, Invalid []string
	}
	Missing []struct {
		Name                  string
		Holds, Asked, Missing []string
	}
}

func readContentParity(t *testing.T) contentParity {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "content_bucket_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parity contentParity
	if err := json.Unmarshal(raw, &parity); err != nil {
		t.Fatal(err)
	}
	if len(parity.Puts) == 0 || len(parity.Hashes.Valid) == 0 || len(parity.Hashes.Invalid) == 0 || len(parity.Missing) == 0 {
		t.Fatal("a section holds no vector")
	}
	return parity
}

// TestContentBucketParity: ContentBucket answers each vector of
// content_bucket_parity.json, as the TypeScript and Rust runtimes' content
// buckets do (D142).
func TestContentBucketParity(t *testing.T) {
	ctx := context.Background()
	parity := readContentParity(t)
	raw := newMemBucket()
	content := NewContent(raw, parity.MaxBytes)
	for _, vector := range parity.Puts {
		body := []byte(vector.Text)
		if vector.Bytes != nil {
			body = make([]byte, len(vector.Bytes))
			for i, b := range vector.Bytes {
				body[i] = byte(b)
			}
		}
		before := len(raw.objects)
		got, err := content.Put(ctx, bytes.NewReader(body), ContentPutOptions{ContentType: "text/plain"})
		if vector.TooLarge {
			if !errors.Is(err, ErrContentTooLarge) || len(raw.objects) != before {
				t.Errorf("%s: %+v, %v, want ErrContentTooLarge and nothing written", vector.Name, got, err)
			}
			continue
		}
		if err != nil || got != (ContentObject{Hash: vector.Hash, Size: vector.Size, Created: vector.Created}) {
			t.Errorf("%s: %+v, %v, want %s, %d bytes, created %t", vector.Name, got, err, vector.Hash, vector.Size, vector.Created)
			continue
		}
		stored, held := raw.objects[vector.Object]
		if !held || !bytes.Equal(stored.data, body) || stored.contentType != "text/plain" {
			t.Errorf("%s: object %s holds %q (%s)", vector.Name, vector.Object, stored.data, stored.contentType)
		}
		reader, err := content.Get(ctx, vector.Hash)
		if err != nil {
			t.Errorf("%s: Get: %v", vector.Name, err)
			continue
		}
		read, _ := io.ReadAll(reader)
		_ = reader.Close()
		if !bytes.Equal(read, body) {
			t.Errorf("%s: Get read %q", vector.Name, read)
		}
	}

	for _, hash := range parity.Hashes.Valid {
		if name, err := ContentName(hash); err != nil || name != ContentPrefix+hash {
			t.Errorf("ContentName(%q) = %q, %v", hash, name, err)
		}
	}
	for _, hash := range parity.Hashes.Invalid {
		if _, err := ContentName(hash); err == nil {
			t.Errorf("ContentName(%q) is no error", hash)
		}
		if _, err := content.Has(ctx, hash); err == nil {
			t.Errorf("Has(%q) is no error", hash)
		}
		if _, err := content.Get(ctx, hash); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v, want a refusal of the hash", hash, err)
		}
		if _, err := content.SignedURL(ctx, hash, time.Minute); err == nil {
			t.Errorf("SignedURL(%q) is no error", hash)
		}
		if _, err := content.Missing(ctx, []string{parity.Hashes.Valid[0], hash}); err == nil {
			t.Errorf("Missing with %q is no error", hash)
		}
	}

	for _, vector := range parity.Missing {
		content := NewContent(newMemBucket(), 0)
		for _, text := range vector.Holds {
			if _, err := content.Put(ctx, strings.NewReader(text), ContentPutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := content.Missing(ctx, vector.Asked)
		if err != nil || !reflect.DeepEqual(got, vector.Missing) {
			t.Errorf("%s: Missing = %v, %v, want %v", vector.Name, got, err, vector.Missing)
		}
	}
}

// TestContentBucket: the default limit, what the bucket hands on, Has and
// Get for bytes it lacks, a signed URL that is a GET alone, and two puts
// of the same bytes at once, of which one creates the object.
func TestContentBucket(t *testing.T) {
	ctx := context.Background()
	raw := newMemBucket()
	content := NewContent(raw, 0)
	if content.MaxBytes() != DefaultContentMaxBytes || DefaultContentMaxBytes != 32<<20 || content.Raw() != Bucket(raw) || content.Name() != "mem" {
		t.Errorf("content bucket = %d bytes over %v", content.MaxBytes(), content.Raw())
	}
	const absent = "0000000000000000000000000000000000000000000000000000000000000000"
	if held, err := content.Has(ctx, absent); err != nil || held {
		t.Errorf("Has of absent bytes = %t, %v", held, err)
	}
	if _, err := content.Get(ctx, absent); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of absent bytes = %v, want ErrNotFound", err)
	}
	put, err := content.Put(ctx, strings.NewReader("hello"), ContentPutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if held, err := content.Has(ctx, put.Hash); err != nil || !held {
		t.Errorf("Has of put bytes = %t, %v", held, err)
	}
	url, err := content.SignedURL(ctx, put.Hash, 5*time.Minute)
	if err != nil || url != "https://mem.example/sha256/"+put.Hash+"?method=GET&expires=300" {
		t.Errorf("SignedURL = %q, %v", url, err)
	}
	if _, err := content.SignedURL(ctx, put.Hash, 0); err == nil {
		t.Error("a signed URL that expires at once was signed")
	}

	// A reader that fails is the error, and nothing is written.
	before := raw.puts
	if _, err := content.Put(ctx, io.MultiReader(strings.NewReader("part"), failingReader{}), ContentPutOptions{}); err == nil || raw.puts != before {
		t.Errorf("a failing body: %v, %d puts", err, raw.puts-before)
	}

	var wg sync.WaitGroup
	created := make([]bool, 32)
	for i := range created {
		wg.Add(1)
		go func() {
			defer wg.Done()
			put, err := content.Put(ctx, strings.NewReader("raced"), ContentPutOptions{})
			if err != nil {
				t.Error(err)
			}
			created[i] = put.Created
		}()
	}
	wg.Wait()
	count := 0
	for _, c := range created {
		if c {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d of %d puts of the same bytes created the object, want 1", count, len(created))
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the body broke") }
