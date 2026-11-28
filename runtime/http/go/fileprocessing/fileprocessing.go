// Package fileprocessing is the file processor's pass in Go (D154, D159,
// D184, #603): the twin of the TypeScript runtime's processFiles
// (runtime/http/typescript/src/file-processing.ts) and of the Rust
// runtime's file_processing::process_files.
//
// Attachments' pending and processed are its seam. An upload's Complete
// asks a file's text (extraction pending), and a file's first view
// (Download) asks its preview (preview pending). Process takes what is
// pending, oldest first, reads each object's bytes from the bucket, and
// hands them to the file processors the environment's files slots name,
// in slot order: the first that reads the media type and answers wins.
// The text is recorded as the file's extractedText on every file that
// names the object, which search's triggers hear and index with its row
// (D155). The preview is put beside the object, at
// rowrefs.PreviewObjectOf, and recorded as the file's preview.
//
// A file is recorded failed, so it is not asked again, when no processor
// reads it, when it holds more than MaxBytes, when its object is gone, or
// when a processor fails on it. The reason is logged without the file's
// bytes or text. A pass does not retry a file within itself, as the
// TypeScript pass does not: a processor that fails once ends the file
// failed. An error of the database is Process's own, which its caller's
// job or worker retries.
//
// It runs within its caller's invocation: a job's or a worker's, or an
// operation's after a write. Each slot's provider runs as its own
// principal (D159), as providers.Providers binds every call into it.
package fileprocessing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/bucket"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// DefaultMaxBytes is the largest object a pass reads unless set: 25 MiB.
const DefaultMaxBytes = 25 * 1024 * 1024

// DefaultLimit is how many objects of each kind one pass takes unless set.
const DefaultLimit = 20

// DefaultPreview is how large a preview may be unless set: 320 by 160.
var DefaultPreview = providers.PreviewOptions{MaxWidth: 320, MaxHeight: 160}

// Attachments are what a pass reads and records of the attachments: a
// generated ORM's Attachments(), on Postgres or on a SQLite file.
type Attachments interface {
	// Pending is the objects whose extraction or preview (kind) a
	// processor has yet to make, each once, oldest first, at most limit.
	Pending(ctx context.Context, kind string, limit int) ([]rowrefs.PendingObject, error)
	// Processed records what a processor made of an object, on every file
	// that names it.
	Processed(ctx context.Context, in rowrefs.ProcessedInput) (int64, error)
}

// Options are what Process reads and writes.
type Options struct {
	// Attachments are the attachments of a database that declares
	// @attachments: its ORM's, or a transaction's.
	Attachments Attachments
	// Providers are the providers the environment configures: every files
	// slot it fills is asked, in slot order.
	Providers *providers.Providers
	// Bucket is the bucket the attachments' objects are in, which a
	// preview is put in too.
	Bucket bucket.Bucket
	// Logger logs each file not processed; nil logs nothing.
	Logger *zap.Logger
	// Preview is how large a preview may be: DefaultPreview unless set.
	Preview providers.PreviewOptions
	// MaxBytes is the largest object read, in bytes: DefaultMaxBytes
	// unless set. A larger one is recorded failed.
	MaxBytes int64
	// Limit is how many objects of each kind a pass takes: DefaultLimit
	// unless set.
	Limit int
}

// Run is what one pass did.
type Run struct {
	// Extracted are the objects whose text was recorded.
	Extracted int `json:"extracted"`
	// Previewed are the objects whose preview was put and recorded.
	Previewed int `json:"previewed"`
	// Failed are the objects recorded failed, of either kind.
	Failed int `json:"failed"`
}

// unprocessable is why an object was not processed, for the log and the
// failed record.
type unprocessable struct{ reason string }

func (u *unprocessable) Error() string { return u.reason }

func refuse(format string, args ...any) error {
	return &unprocessable{reason: fmt.Sprintf(format, args...)}
}

// Process makes one pass over what is pending: up to Limit objects whose
// text has yet to be extracted, then up to Limit whose preview has yet to
// be made.
func Process(ctx context.Context, options Options) (Run, error) {
	var run Run
	if options.Attachments == nil {
		return run, errors.New("fileprocessing: Process needs the attachments of a database that declares @attachments")
	}
	if options.Bucket == nil {
		return run, errors.New("fileprocessing: Process needs the bucket the attachments' objects are in")
	}
	log := options.Logger
	if log == nil {
		log = zap.NewNop()
	}
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	preview := options.Preview
	if preview == (providers.PreviewOptions{}) {
		preview = DefaultPreview
	}
	var slots []string
	if options.Providers != nil {
		for _, c := range options.Providers.Configured(providers.Files) {
			slots = append(slots, c.Slot)
		}
	}
	processors := func(mediaType string) ([]providers.FileProcessor, error) {
		var out []providers.FileProcessor
		for _, slot := range slots {
			processor, err := options.Providers.Files(ctx, slot)
			if err != nil {
				return nil, err
			}
			if providers.ReadsMediaType(processor.MediaTypes(), mediaType) {
				out = append(out, processor)
			}
		}
		return out, nil
	}
	for _, kind := range []string{rowrefs.KindExtraction, rowrefs.KindPreview} {
		pending, err := options.Attachments.Pending(ctx, kind, limit)
		if err != nil {
			return run, err
		}
		for _, file := range pending {
			err := processOne(ctx, options, file, kind, maxBytes, preview, processors)
			if err == nil {
				if kind == rowrefs.KindExtraction {
					run.Extracted++
				} else {
					run.Previewed++
				}
				continue
			}
			log.Warn("a file was not processed", zap.String("objectKey", file.ObjectKey), zap.String("mediaType", file.MediaType), zap.String("kind", kind), zap.String("reason", err.Error()))
			failed := &rowrefs.ProcessedPart{State: rowrefs.ProcessingFailed}
			in := rowrefs.ProcessedInput{ObjectKey: file.ObjectKey, Extraction: failed}
			if kind == rowrefs.KindPreview {
				in = rowrefs.ProcessedInput{ObjectKey: file.ObjectKey, Preview: failed}
			}
			if _, err := options.Attachments.Processed(ctx, in); err != nil {
				return run, err
			}
			run.Failed++
		}
	}
	return run, nil
}

// processOne makes one object's text or preview and records it: an error
// is why it was not, which Process records failed.
func processOne(ctx context.Context, options Options, file rowrefs.PendingObject, kind string, maxBytes int64, preview providers.PreviewOptions, processors func(string) ([]providers.FileProcessor, error)) error {
	readers, err := processors(file.MediaType)
	if err != nil {
		return err
	}
	if len(readers) == 0 {
		return refuse("no files slot of the environment reads %s", file.MediaType)
	}
	data, err := readObject(ctx, options.Bucket, file.ObjectKey, maxBytes)
	if err != nil {
		return err
	}
	input := providers.FileInput{MediaType: file.MediaType, Bytes: data}
	if kind == rowrefs.KindExtraction {
		var text *string
		for _, processor := range readers {
			answer, err := processor.ExtractText(ctx, input)
			if err != nil {
				return err
			}
			if answer != nil {
				text = &answer.Text
				break
			}
		}
		if text == nil {
			return refuse("no file processor extracts the text of %s", file.MediaType)
		}
		_, err := options.Attachments.Processed(ctx, rowrefs.ProcessedInput{ObjectKey: file.ObjectKey, Extraction: &rowrefs.ProcessedPart{State: rowrefs.ProcessingDone, Text: text}})
		return err
	}
	var made *providers.Preview
	for _, processor := range readers {
		answer, err := processor.Preview(ctx, input, preview)
		if err != nil {
			return err
		}
		if answer != nil {
			made = answer
			break
		}
	}
	if made == nil {
		return refuse("no file processor previews %s", file.MediaType)
	}
	object := rowrefs.PreviewObjectOf(file.ObjectKey)
	if _, err := options.Bucket.Put(ctx, object, bytes.NewReader(made.Bytes), bucket.PutOptions{ContentType: made.MediaType}); err != nil {
		return err
	}
	_, err = options.Attachments.Processed(ctx, rowrefs.ProcessedInput{ObjectKey: file.ObjectKey, Preview: &rowrefs.ProcessedPart{State: rowrefs.ProcessingDone, Object: &object}})
	return err
}

// readObject is the bytes of the object name, refused past maxBytes or
// when it is gone.
func readObject(ctx context.Context, b bucket.Bucket, name string, maxBytes int64) ([]byte, error) {
	r, err := b.Get(ctx, name)
	if errors.Is(err, bucket.ErrNotFound) {
		return nil, refuse("object %s is gone", name)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if r.Object.Size > maxBytes {
		return nil, refuse("object %s holds %d bytes, more than the %d a pass reads", name, r.Object.Size, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, refuse("object %s holds more than the %d bytes a pass reads", name, maxBytes)
	}
	return data, nil
}
