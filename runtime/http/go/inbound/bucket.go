package inbound

import (
	"bytes"
	"context"

	"github.com/parable-work/superschematic/runtime/http/go/bucket"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// BucketOf is a Bucket of b, an environment's bucket: attachments sign
// URLs for it and read and delete its objects (bucket.ForAttachments), and
// the path writes an inbound message's files to it.
func BucketOf(b bucket.Bucket) Bucket {
	return files{Bucket: bucket.ForAttachments(b), raw: b}
}

type files struct {
	rowrefs.Bucket
	raw bucket.Bucket
}

func (f files) PutObject(ctx context.Context, name, contentType string, body []byte) error {
	_, err := f.raw.Put(ctx, name, bytes.NewReader(body), bucket.PutOptions{ContentType: contentType})
	return err
}
