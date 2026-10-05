package versiongraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type vector struct {
	Name   string          `json:"name"`
	Op     string          `json:"op"`
	Input  json.RawMessage `json:"input"`
	Expect json.RawMessage `json:"expect"`
}

var ops = map[string]op{
	"compose":      opCompose,
	"merge":        opMerge,
	"diff":         opDiff,
	"content_hash": opContentHash,
	"validate":     opValidate,
}

// TestVectors runs every vector through the C ABI and compares the output
// document with the vector's expectation, then runs it again through the typed
// function when the input decodes into the typed request unchanged.
func TestVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "testdata", "vectors", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no vectors found")
	}
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(text, &v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		t.Run(v.Name, func(t *testing.T) {
			o, known := ops[v.Op]
			if !known {
				t.Fatalf("unknown op %q", v.Op)
			}
			var in bytes.Buffer
			if err := json.Compact(&in, v.Input); err != nil {
				t.Fatal(err)
			}
			out, _, err := call(o, in.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			assertSameJSON(t, "call", v.Expect, out)

			switch v.Op {
			case "compose":
				checkTyped(t, v, Compose)
			case "merge":
				checkTyped(t, v, Merge)
			case "diff":
				checkTyped(t, v, Diff)
			case "content_hash":
				checkTyped(t, v, ContentHash)
			case "validate":
				checkTyped(t, v, Validate)
			}
		})
	}
}

// checkTyped decodes the vector's input into Req and, when that loses
// nothing, runs fn and compares its result (or {"error": ...}) with the
// expectation.
func checkTyped[Req, Res any](t *testing.T, v vector, fn func(Req) (*Res, error)) {
	t.Helper()
	var req Req
	if err := json.Unmarshal(v.Input, &req); err != nil {
		return
	}
	again, err := json.Marshal(req)
	if err != nil || !sameJSON(v.Input, again) {
		return
	}
	res, err := fn(req)
	var got any = res
	if err != nil {
		var coreErr *Error
		if !errors.As(err, &coreErr) {
			t.Fatalf("typed: %v", err)
		}
		got = map[string]*Error{"error": coreErr}
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "typed", v.Expect, out)
}

func TestErrorIsReturnedAsError(t *testing.T) {
	descriptor := `{"version":3,"root":{"table":"recipe","key":"id"},"refTable":"recipe_ref",` +
		`"commitTable":"recipe_commit","patchTable":"recipe_patch","releaseTable":"recipe_release",` +
		`"snapshotTable":"recipe_snapshot_entry","kinds":[]}`
	_, err := Validate(TreeRequest{Descriptor: json.RawMessage(descriptor), Tree: json.RawMessage(`[]`)})
	var coreErr *Error
	if !errors.As(err, &coreErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if coreErr.Code != "invalid_request" || !strings.Contains(err.Error(), "a tree is a JSON object") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestVersion2DescriptorIsRefused: the fixture's descriptor as version 2
// wrote it, without each kind's history, is invalid_descriptor.
func TestVersion2DescriptorIsRefused(t *testing.T) {
	text, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(text, &descriptor); err != nil {
		t.Fatal(err)
	}
	descriptor["version"] = 2
	for _, kind := range descriptor["kinds"].([]any) {
		delete(kind.(map[string]any), "history")
	}
	v2, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Validate(TreeRequest{Descriptor: v2, Tree: json.RawMessage(`{}`)})
	var coreErr *Error
	if !errors.As(err, &coreErr) || coreErr.Code != "invalid_descriptor" || !strings.Contains(err.Error(), "version 2 is not supported") {
		t.Fatalf("Validate of a version 2 descriptor = %v, want invalid_descriptor", err)
	}
}

func TestEmptyInputIsRefused(t *testing.T) {
	out, ok, err := call(opDiff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("empty input accepted: %s", out)
	}
	if err := decodeError(out); err == nil || !strings.Contains(err.Error(), "invalid_json") {
		t.Fatalf("want invalid_json, got %v", err)
	}
}

func sameJSON(a, b []byte) bool {
	x, errX := decode(a)
	y, errY := decode(b)
	return errX == nil && errY == nil && reflect.DeepEqual(x, y)
}

func assertSameJSON(t *testing.T, label string, want, got []byte) {
	t.Helper()
	if !sameJSON(want, got) {
		t.Fatalf("%s: output differs\nwant: %s\ngot:  %s", label, compact(want), got)
	}
}

// decode reads JSON keeping numbers as written, so wide integers compare
// exactly.
func decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}

func compact(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}
