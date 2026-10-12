// Package bucket is the provider-neutral interface of a bucket an API lists
// in its config's buckets (docs/stack-model.md, section 8.9, D54): private
// object storage the API's implementation puts, gets, deletes and lists
// objects in, and signs URLs for, so that a browser uploads or downloads an
// object directly. The generated Deps holds a Bucket per listed bucket, and
// the generated entrypoint fills it with the implementation the bucket's
// provider needs: GCS's, which a server some environment reaches a bucket
// from links, and which reaches the local target's emulator too. No
// provider's client is in this package, so a module that imports it
// requires none.
package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Bucket is one bucket. Object names are slash-separated paths, such as
// `products/7b0e/image.png`; a bucket has no directories, and List's prefix
// selects the names that begin with it.
type Bucket interface {
	// Name is the bucket's name with its provider.
	Name() string

	// Put writes the object name from body, replacing any object of that
	// name, and returns what the provider stored. It streams body: the
	// object is whole once Put returns, and absent if it fails.
	Put(ctx context.Context, name string, body io.Reader, opts PutOptions) (Object, error)

	// Get opens the object name for reading. The caller closes the
	// Reader. ErrNotFound when there is no such object.
	Get(ctx context.Context, name string) (*Reader, error)

	// Delete removes the object name. ErrNotFound when there is no such
	// object.
	Delete(ctx context.Context, name string) error

	// List returns a page of the objects whose names begin with
	// opts.Prefix, in name order, and the token of the next page, empty
	// on the last.
	List(ctx context.Context, opts ListOptions) (Page, error)

	// SignedURL returns a URL that lets whoever holds it, with no other
	// credential, GET the object name or PUT it, until opts.Expires
	// passes. A PUT through the URL must send opts.ContentType, when it
	// is set, as its Content-Type.
	SignedURL(ctx context.Context, name string, opts SignedURLOptions) (string, error)
}

// ErrNotFound is the error Get and Delete wrap when there is no such
// object.
var ErrNotFound = errors.New("bucket: no such object")

// Object is what a bucket holds of an object beside its bytes.
type Object struct {
	// Name is the object's name in the bucket.
	Name string

	// Size is its length in bytes.
	Size int64

	// ContentType is its media type, as Put or the signed URL's PUT gave
	// it.
	ContentType string

	// Updated is when it was last written.
	Updated time.Time
}

// Reader reads one object's bytes; Object is what the bucket holds of it.
type Reader struct {
	io.ReadCloser
	Object Object
}

// PutOptions are what Put writes beside the bytes.
type PutOptions struct {
	// ContentType is the object's media type; empty lets the provider
	// pick, application/octet-stream on GCS.
	ContentType string
}

// ListOptions select a page of objects.
type ListOptions struct {
	// Prefix selects the objects whose names begin with it; empty selects
	// every object.
	Prefix string

	// PageToken is a Page's NextPageToken, to read the page after it;
	// empty reads the first.
	PageToken string

	// PageSize bounds the objects a page holds; zero is DefaultPageSize.
	PageSize int
}

// DefaultPageSize is the size of a page whose ListOptions set none.
const DefaultPageSize = 1000

// Page is one page of a List.
type Page struct {
	// Objects are the page's objects, in name order.
	Objects []Object

	// NextPageToken reads the next page; empty on the last.
	NextPageToken string
}

// The methods a signed URL allows.
const (
	MethodGet = "GET"
	MethodPut = "PUT"
)

// MaxSignedURLExpiry is the longest a signed URL lives: seven days, GCS's
// limit for a V4 signature.
const MaxSignedURLExpiry = 7 * 24 * time.Hour

// SignedURLOptions are what a signed URL allows.
type SignedURLOptions struct {
	// Method is MethodGet, to download the object, or MethodPut, to
	// upload it.
	Method string

	// Expires is how long the URL lives, from now: more than zero, and at
	// most MaxSignedURLExpiry.
	Expires time.Duration

	// ContentType, for a PUT, is the Content-Type the upload must send,
	// which the signature covers. Empty leaves it to the uploader.
	ContentType string
}

// Check refuses options no provider signs: another method, an expiry out
// of range, and a content type on a GET.
func (o SignedURLOptions) Check() error {
	switch {
	case o.Method != MethodGet && o.Method != MethodPut:
		return fmt.Errorf("bucket: a signed URL's method is %q; want %s or %s", o.Method, MethodGet, MethodPut)
	case o.Expires <= 0 || o.Expires > MaxSignedURLExpiry:
		return fmt.Errorf("bucket: a signed URL expires after %s; want more than none and at most %s", o.Expires, MaxSignedURLExpiry)
	case o.Method == MethodGet && o.ContentType != "":
		return fmt.Errorf("bucket: a signed URL for a %s names a content type, which only an upload sends", MethodGet)
	}
	return nil
}

// CheckName refuses an object name no provider stores: an empty one, one
// longer than 1024 bytes, or one with a carriage return, a line feed or a
// NUL.
func CheckName(name string) error {
	switch {
	case name == "":
		return errors.New("bucket: an object's name is empty")
	case len(name) > 1024:
		return fmt.Errorf("bucket: an object's name is %d bytes; at most 1024", len(name))
	case strings.ContainsAny(name, "\r\n\x00"):
		return fmt.Errorf("bucket: object name %q holds a carriage return, a line feed or a NUL", name)
	}
	return nil
}
