package pygen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestDiscriminatedUnionUsesPythonFieldName pins that a discriminated
// union names its discriminator by the member models' Python field name.
// Pydantic resolves Field(discriminator=...) against field names, so a
// camelCase discriminator (eventKind) written as is made the union fail
// to build.
func TestDiscriminatedUnionUsesPythonFieldName(t *testing.T) {
	schema := ir.NewSchema("event-union", ir.SchemaKindGeneral)
	created, deleted := "created", "deleted"
	schema.Types["CreatedEvent"] = &ir.TypeDef{
		Name: "CreatedEvent",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "eventKind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &created},
			{Name: "itemId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["DeletedEvent"] = &ir.TypeDef{
		Name: "DeletedEvent",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "eventKind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &deleted},
			{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Unions["Event"] = &ir.UnionDef{Name: "Event", Types: []string{"CreatedEvent", "DeletedEvent"}}
	schema.Types["EventEnvelope"] = &ir.TypeDef{
		Name: "EventEnvelope",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "event", TypeRef: ir.TypeRef{Name: "Event"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{SchemaName: "event-union"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	unions, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "unions.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unions), `Field(discriminator="event_kind")`) {
		t.Fatalf("unions.py does not name the discriminator by its Python field:\n%s", unions)
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; generated union assertions passed")
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic unavailable; generated union assertions passed")
	}
	command := exec.Command(python, "-B", "-c", `
import importlib
import sys

module = importlib.import_module(sys.argv[1])
envelope = module.EventEnvelope.model_validate({"event": {"eventKind": "deleted", "reason": "expired"}})
assert type(envelope.event).__name__ == "DeletedEvent", type(envelope.event)
envelope = module.EventEnvelope.model_validate({"event": {"eventKind": "created", "itemId": "a1"}})
assert type(envelope.event).__name__ == "CreatedEvent", type(envelope.event)
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated discriminated union failed: %v\n%s", err, out)
	}
}
