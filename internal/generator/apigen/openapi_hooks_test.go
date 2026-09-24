package apigen_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

func generateFixtureAPIWithHooks(t *testing.T, hooks ...apigen.OpenAPIHook) (*apigen.APIOutput, error) {
	t.Helper()
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	return apigen.Generate(apiSchema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-api",
		ModulePath:     "example.com/schemas/api/fixture-api",
		TypesModule:    "example.com/schemas/types/go/fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		OpenAPIHooks:   hooks,
	})
}

// TestOpenAPIHookThatChangesNothingKeepsTheDocument: the hooks see the
// document as decoded JSON; writing it back out after a hook that edits
// nothing gives the same bytes as a run with no hook.
func TestOpenAPIHookThatChangesNothingKeepsTheDocument(t *testing.T) {
	plain, err := generateFixtureAPIWithHooks(t)
	if err != nil {
		t.Fatal(err)
	}
	var sawSchema string
	hooked, err := generateFixtureAPIWithHooks(t, apigen.OpenAPIHook{
		Name: "observe",
		Edit: func(schema *ir.Schema, doc map[string]any) error {
			sawSchema = schema.Name
			if _, ok := doc["paths"].(map[string]any); !ok {
				t.Errorf("paths is %T, want map[string]any", doc["paths"])
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawSchema != "fixture-api" {
		t.Errorf("hook saw schema %q", sawSchema)
	}
	if hooked.OpenAPISpecRaw != plain.OpenAPISpecRaw {
		t.Fatal("a hook that changes nothing changed the OpenAPI document")
	}
}

// TestOpenAPIHooksEditTheWrittenDocumentInOrder: every hook's edit reaches
// openapi.json and the embedded copy, and a later hook sees an earlier
// hook's edit.
func TestOpenAPIHooksEditTheWrittenDocumentInOrder(t *testing.T) {
	output, err := generateFixtureAPIWithHooks(t,
		apigen.OpenAPIHook{Name: "add", Edit: func(_ *ir.Schema, doc map[string]any) error {
			doc["x-first"] = map[string]any{"n": json.Number("1")}
			return nil
		}},
		apigen.OpenAPIHook{Name: "rename", Edit: func(_ *ir.Schema, doc map[string]any) error {
			value, ok := doc["x-first"]
			if !ok {
				return errors.New("the first hook's key is missing")
			}
			delete(doc, "x-first")
			doc["x-second"] = value
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["x-first"]; ok {
		t.Error("x-first survived the rename")
	}
	if got, ok := doc["x-second"].(map[string]any); !ok || got["n"] != float64(1) {
		t.Errorf("x-second = %#v", doc["x-second"])
	}
	if !strings.Contains(output.OpenAPISpec, `"x-second"`) {
		t.Error("the embedded spec lacks the hook's edit")
	}
}

func TestOpenAPIHookErrorNamesTheHook(t *testing.T) {
	_, err := generateFixtureAPIWithHooks(t, apigen.OpenAPIHook{
		Name: "strict",
		Edit: func(*ir.Schema, map[string]any) error { return errors.New("no vendor key") },
	})
	if err == nil || !strings.Contains(err.Error(), "OpenAPI hook strict: no vendor key") {
		t.Fatalf("err = %v, want the hook named", err)
	}
}
