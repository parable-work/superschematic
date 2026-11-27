package providers

import (
	"context"
	"strings"
)

// The file processor (D159): what attachments (D154) and search (D155)
// call on a stored file. It extracts the file's text and makes a preview,
// an image a list shows in place of the file. A processor names the media
// types it reads; for any other it answers nil, and core tries the next
// one.

// FileInput is a file to process: its bytes and what they are.
type FileInput struct {
	MediaType string `json:"mediaType"`
	Bytes     []byte `json:"bytes"`
	Name      string `json:"name,omitempty"`
}

// ExtractedText is a file's text.
type ExtractedText struct {
	Text string `json:"text"`
	// Pages are the pages it came from, for a paged document.
	Pages int `json:"pages,omitempty"`
	// Truncated reports text that stops short of the file's.
	Truncated bool `json:"truncated,omitempty"`
}

// PreviewOptions are how large a preview may be.
type PreviewOptions struct {
	MaxWidth  int `json:"maxWidth,omitempty"`
	MaxHeight int `json:"maxHeight,omitempty"`
}

// Preview is a preview: an image's bytes and what they are.
type Preview struct {
	MediaType string `json:"mediaType"`
	Bytes     []byte `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// FileProcessor is a file processor.
type FileProcessor interface {
	// MediaTypes are the media types it reads, each a type or a family
	// ("image/*").
	MediaTypes() []string
	// ExtractText is the file's text, or nil for a file it does not read.
	ExtractText(ctx context.Context, file FileInput) (*ExtractedText, error)
	// Preview is the file's preview, or nil for a file it does not
	// preview.
	Preview(ctx context.Context, file FileInput, options PreviewOptions) (*Preview, error)
}

// ReadsMediaType reports whether a processor that reads mediaTypes reads
// a file of mediaType: a type it names, or one of a family it names,
// parameters left out.
func ReadsMediaType(mediaTypes []string, mediaType string) bool {
	kind, _, _ := strings.Cut(mediaType, ";")
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, entry := range mediaTypes {
		want := strings.ToLower(entry)
		if want == kind || (strings.HasSuffix(want, "/*") && strings.HasPrefix(kind, strings.TrimSuffix(want, "*"))) {
			return true
		}
	}
	return false
}
