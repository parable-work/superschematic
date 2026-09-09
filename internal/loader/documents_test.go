package loader

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// simpleWidgetSchema is a one-type schema file for the manifest tests.
const simpleWidgetSchema = `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}}]}`

// manifestSchema is the JSON Schema the manifest document's data loader
// validates against.
const manifestSchema = `{"type":"object","required":["replicas"],"properties":{"replicas":{"type":"integer"}},"additionalProperties":false}`

// manifestRegistry is the core plus one YAML-backed sidecar document,
// optionally restricted to kinds.
func manifestRegistry(t *testing.T, kinds ...string) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	err := reg.RegisterDocument(registry.DocumentSpec{
		Name:      "manifest",
		Extension: "acme",
		File:      "manifest.yaml",
		Kinds:     kinds,
		Schema:    json.RawMessage(manifestSchema),
		Loader: func(_ context.Context, lc registry.LoadContext) (json.RawMessage, []string, error) {
			doc, err := lc.DecodeData("manifest.yaml", json.RawMessage(manifestSchema))
			if err != nil {
				return nil, nil, err
			}
			return doc, []string{"/imports/manifest-shared.yaml"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestLoadServiceStoresRegisteredDocumentsCanonically pins the document
// loop: a registered sidecar is decoded through the load context, stored
// under Schema.Documents in canonical JSON, and its imports join the
// schema's authoring imports.
func TestLoadServiceStoresRegisteredDocumentsCanonically(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  simpleWidgetSchema,
		"manifest.yaml":      "replicas: 2\n",
	})
	schema, err := LoadService(dir, WithRegistry(manifestRegistry(t)))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if got := string(schema.Documents["manifest"]); got != `{"replicas":2}` {
		t.Errorf("Documents[manifest] = %s, want canonical {\"replicas\":2}", got)
	}
	if len(schema.AuthoringImports) != 1 || schema.AuthoringImports[0] != "/imports/manifest-shared.yaml" {
		t.Errorf("AuthoringImports = %v, want the loader's import", schema.AuthoringImports)
	}
}

// TestLoadServiceSkipsAbsentDocuments pins that discovery is by file
// presence: no sidecar, no loader call, no document.
func TestLoadServiceSkipsAbsentDocuments(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  simpleWidgetSchema,
	})
	schema, err := LoadService(dir, WithRegistry(manifestRegistry(t)))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if len(schema.Documents) != 0 || len(schema.AuthoringImports) != 0 {
		t.Errorf("Documents = %v, AuthoringImports = %v, want none", schema.Documents, schema.AuthoringImports)
	}
}

// TestLoadServiceValidatesDataDocuments pins that DecodeData applies the
// spec's JSON Schema and names the file.
func TestLoadServiceValidatesDataDocuments(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  simpleWidgetSchema,
		"manifest.yaml":      "replicas: two\n",
	})
	_, err := LoadService(dir, WithRegistry(manifestRegistry(t)))
	if err == nil {
		t.Fatal("LoadService accepted a manifest that fails its schema")
	}
	if !strings.Contains(err.Error(), "manifest.yaml") || !strings.Contains(err.Error(), "replicas") {
		t.Errorf("error does not name the file and field: %v", err)
	}
}

// TestLoadServiceRejectsDocumentsInDisallowedKinds pins Kinds: a sidecar
// present in a kind the spec does not admit is an error, not a silent skip.
func TestLoadServiceRejectsDocumentsInDisallowedKinds(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  simpleWidgetSchema,
		"manifest.yaml":      "replicas: 2\n",
	})
	_, err := LoadService(dir, WithRegistry(manifestRegistry(t, "API")))
	if err == nil {
		t.Fatal("LoadService accepted a manifest in a General schema when the spec admits only API")
	}
	if !strings.Contains(err.Error(), "manifest.yaml: document manifest is not allowed in General schemas (allowed kinds: API)") {
		t.Errorf("error = %v", err)
	}
}

// TestLoadServiceRejectsSidecarDuplicatingDataFormDocument pins that a
// sidecar for a document the schema files already define inline is a
// conflict.
func TestLoadServiceRejectsSidecarDuplicatingDataFormDocument(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json":   minimalConfig,
		"src/a.schema.json":    simpleWidgetSchema,
		"src/docs.schema.json": `{"documents": {"manifest": {"replicas": 3}}}`,
		"manifest.yaml":        "replicas: 2\n",
	})
	_, err := LoadService(dir, WithRegistry(manifestRegistry(t)))
	if err == nil {
		t.Fatal("LoadService accepted a sidecar next to an inline documents.manifest")
	}
	if !strings.Contains(err.Error(), "document manifest is already defined by the schema files") {
		t.Errorf("error = %v", err)
	}
}
