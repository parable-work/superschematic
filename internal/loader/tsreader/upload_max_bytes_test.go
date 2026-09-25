package tsreader

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func TestApplyValidateConfigPreservesUploadMaxBytes(t *testing.T) {
	field := &ir.FieldDef{}
	applyValidateConfig(map[string]any{"uploadMaxBytes": int64(64 * 1024 * 1024)}, field)
	if field.ValidateUploadMaxBytes == nil || *field.ValidateUploadMaxBytes != 64*1024*1024 {
		t.Fatalf("upload max bytes = %v, want 67108864", field.ValidateUploadMaxBytes)
	}
}

func TestUploadMaxBytesLiteralRejectsLossyNumbers(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int64
		ok    bool
	}{
		{name: "ordinary", value: float64(64 * 1024 * 1024), want: 64 * 1024 * 1024, ok: true},
		{name: "maximum safe integer", value: float64(9007199254740991), want: 9007199254740991, ok: true},
		{name: "fractional", value: 1.5},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "not a number", value: math.NaN()},
		{name: "unsafe integer", value: float64(9007199254740992)},
		{name: "string", value: "1024"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := uploadMaxBytesLiteral(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("uploadMaxBytes literal = (%d, %t), want (%d, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}

// The file-upload scalar check runs after the loader hydrates the scalars
// (internal/loader, TestUploadMaxBytesOnNonUploadFieldFails): a brand
// carries only its name here. The TypeScript frontend records the bound.
func TestUploadMaxBytesIsRecordedUnchecked(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "broken-upload-max-bytes"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	label := schema.Types["Attachment"].Fields[0]
	if label.Name != "label" || label.ValidateUploadMaxBytes == nil || *label.ValidateUploadMaxBytes != 1024 {
		t.Fatalf("Attachment.label = %+v, want uploadMaxBytes 1024 recorded", label)
	}
}

func TestUploadMaxBytesFractionalLiteralFails(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-upload-max-bytes-lossy"))
	if err == nil || !strings.Contains(err.Error(), "uploadMaxBytes must be a finite JavaScript-safe integer literal") {
		t.Fatalf("want the literal error, got %v", err)
	}
}
