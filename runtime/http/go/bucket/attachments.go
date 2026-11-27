package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// ForAttachments is b as the generated ORM's attachments read it (D154,
// D184): the bucket a browser uploads a row's files to through a URL the
// ORM signs, which the ORM reads back to hash, and deletes an object from
// once no file names it.
func ForAttachments(b Bucket) rowrefs.Bucket { return attachmentsBucket{b} }

type attachmentsBucket struct{ b Bucket }

func (a attachmentsBucket) SignedURL(ctx context.Context, name, method string, expires time.Duration, contentType string) (string, error) {
	return a.b.SignedURL(ctx, name, SignedURLOptions{Method: method, Expires: expires, ContentType: contentType})
}

func (a attachmentsBucket) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	r, err := a.b.Get(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: %w", rowrefs.ErrObjectNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (a attachmentsBucket) Delete(ctx context.Context, name string) error {
	err := a.b.Delete(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w", rowrefs.ErrObjectNotFound, err)
	}
	return err
}
