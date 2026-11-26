package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// DefaultContentMaxBytes is the largest object a content bucket puts when
// its Bucket service names none: 32 MiB.
const DefaultContentMaxBytes = 32 << 20

// ContentPrefix is the prefix of a content bucket's object names, before
// the hash.
const ContentPrefix = "sha256/"

// ErrContentTooLarge is the error a content bucket's Put wraps for a body
// longer than its MaxBytes. It has written nothing.
var ErrContentTooLarge = errors.New("bucket: the content is larger than the content bucket puts")

// ContentObject is what a content bucket's Put answers.
type ContentObject struct {
	// Hash is the SHA-256 of the bytes, 64 lowercase hex digits: what
	// names them from then on.
	Hash string
	// Size is their length in bytes.
	Size int64
	// Created reports whether this Put wrote the object; false when the
	// bucket already held these bytes.
	Created bool
}

// ContentPutOptions are what a content bucket's Put writes beside the
// bytes.
type ContentPutOptions struct {
	// ContentType is the object's media type; empty lets the provider
	// pick.
	ContentType string
}

// ContentBucket is a bucket that names each object by its content (D142):
// the Deps member of a Bucket service whose config says contentAddressed.
// An object is `sha256/<hex>`, the SHA-256 of its bytes, so the same bytes
// are stored once however often they are put, and an object never
// changes. It has no delete; Raw is the plain interface for the caller
// that needs one.
type ContentBucket struct {
	raw      Bucket
	maxBytes int64
}

// NewContent is the content bucket over raw, which the generated
// entrypoint builds for a Bucket service whose config says
// contentAddressed, with its maxBytes; none, or less, is
// DefaultContentMaxBytes.
func NewContent(raw Bucket, maxBytes int64) *ContentBucket {
	if maxBytes <= 0 {
		maxBytes = DefaultContentMaxBytes
	}
	return &ContentBucket{raw: raw, maxBytes: maxBytes}
}

// Name is the bucket's name with its provider.
func (c *ContentBucket) Name() string { return c.raw.Name() }

// MaxBytes is the largest object Put accepts, in bytes.
func (c *ContentBucket) MaxBytes() int64 { return c.maxBytes }

// Raw is the plain bucket, whose names are sha256/<hex>.
func (c *ContentBucket) Raw() Bucket { return c.raw }

// ContentName is the object name of a content hash. It refuses anything
// but 64 lowercase hex digits, so no caller's text reaches a name.
func ContentName(hash string) (string, error) {
	valid := len(hash) == sha256.Size*2
	for i := 0; valid && i < len(hash); i++ {
		valid = (hash[i] >= '0' && hash[i] <= '9') || (hash[i] >= 'a' && hash[i] <= 'f')
	}
	if !valid {
		return "", fmt.Errorf("bucket: a content hash is 64 lowercase hex digits, the SHA-256 of the bytes, not %q", hash)
	}
	return ContentPrefix + hash, nil
}

// Put reads body whole, up to MaxBytes, hashes it and writes it under its
// hash if no object has that name. A longer body is ErrContentTooLarge,
// and nothing is written.
func (c *ContentBucket) Put(ctx context.Context, body io.Reader, opts ContentPutOptions) (ContentObject, error) {
	data, err := io.ReadAll(io.LimitReader(body, c.maxBytes+1))
	if err != nil {
		return ContentObject{}, fmt.Errorf("bucket %s: read the content: %w", c.raw.Name(), err)
	}
	if int64(len(data)) > c.maxBytes {
		return ContentObject{}, fmt.Errorf("bucket %s: at most %d bytes: %w", c.raw.Name(), c.maxBytes, ErrContentTooLarge)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	put, err := c.raw.Put(ctx, ContentPrefix+hash, bytes.NewReader(data), PutOptions{ContentType: opts.ContentType, IfAbsent: true})
	if err != nil {
		return ContentObject{}, err
	}
	return ContentObject{Hash: hash, Size: int64(len(data)), Created: put.Created}, nil
}

// Has reports whether the bucket holds the bytes of hash.
func (c *ContentBucket) Has(ctx context.Context, hash string) (bool, error) {
	name, err := ContentName(hash)
	if err != nil {
		return false, err
	}
	_, held, err := c.raw.Head(ctx, name)
	return held, err
}

// missingConcurrency is how many heads Missing runs at once.
const missingConcurrency = 16

// Missing returns the hashes of hashes the bucket does not hold, each
// once, in the order given.
func (c *ContentBucket) Missing(ctx context.Context, hashes []string) ([]string, error) {
	asked := make([]string, 0, len(hashes))
	seen := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		if _, err := ContentName(hash); err != nil {
			return nil, err
		}
		if !seen[hash] {
			seen[hash] = true
			asked = append(asked, hash)
		}
	}
	held := make([]bool, len(asked))
	errs := make([]error, len(asked))
	limit := make(chan struct{}, missingConcurrency)
	var wg sync.WaitGroup
	for i, hash := range asked {
		wg.Add(1)
		limit <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			held[i], errs[i] = c.Has(ctx, hash)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	missing := []string{}
	for i, hash := range asked {
		if !held[i] {
			missing = append(missing, hash)
		}
	}
	return missing, nil
}

// Get opens the bytes of hash for reading. The caller closes the Reader.
// ErrNotFound when the bucket does not hold them.
func (c *ContentBucket) Get(ctx context.Context, hash string) (*Reader, error) {
	name, err := ContentName(hash)
	if err != nil {
		return nil, err
	}
	return c.raw.Get(ctx, name)
}

// SignedURL returns a URL that lets whoever holds it GET the bytes of hash
// until expires passes. It signs no upload: an object is written only by
// Put, which hashes it.
func (c *ContentBucket) SignedURL(ctx context.Context, hash string, expires time.Duration) (string, error) {
	name, err := ContentName(hash)
	if err != nil {
		return "", err
	}
	return c.raw.SignedURL(ctx, name, SignedURLOptions{Method: "GET", Expires: expires})
}
