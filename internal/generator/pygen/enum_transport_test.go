package pygen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedEnumAcceptsSerializedValuesUnderStrictValidation pins that a
// generated enum accepts its wire value when a model is validated with
// strict=True (Pydantic otherwise demands an enum instance for Python
// input), while unknown values and unrelated coercions still fail.
func TestGeneratedEnumAcceptsSerializedValuesUnderStrictValidation(t *testing.T) {
	schema := ir.NewSchema("enum-transport", ir.SchemaKindGeneral)
	schema.Enums["ProtocolVersion"] = &ir.EnumDef{
		Name: "ProtocolVersion",
		Values: []ir.EnumValueDef{
			{Name: "V1", SerializedAs: "v1"},
		},
	}
	schema.Types["Dispatch"] = &ir.TypeDef{
		Name: "Dispatch",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "protocolVersion", TypeRef: ir.TypeRef{Name: "ProtocolVersion"}, Required: true},
			{Name: "acceptedVersions", TypeRef: ir.TypeRef{Name: "ProtocolVersion", IsArray: true}, Required: true},
			{Name: "versionByService", TypeRef: ir.TypeRef{Name: "ProtocolVersion", IsMap: true}, Required: true},
			{Name: "attempt", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "enum-transport",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	enumsPy, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "enums.py"))
	if err != nil {
		t.Fatalf("read generated enums.py: %v", err)
	}
	for _, expected := range []string{
		"def __get_pydantic_core_schema__(cls, source_type, handler):",
		"core_schema.no_info_before_validator_function(",
	} {
		if !strings.Contains(string(enumsPy), expected) {
			t.Fatalf("enums.py missing %q:\n%s", expected, enumsPy)
		}
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; generated enum assertions passed")
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic unavailable; generated enum assertions passed")
	}
	command := exec.Command(python, "-B", "-c", `
import importlib
import sys

from pydantic import ValidationError

Dispatch = importlib.import_module(sys.argv[1]).Dispatch

payload = {
    "protocolVersion": "v1",
    "acceptedVersions": ["v1"],
    "versionByService": {"api": "v1"},
    "attempt": 1,
}
value = Dispatch.model_validate(payload, strict=True)
assert value.protocol_version == "v1"
assert value.accepted_versions == ["v1"]
assert value.version_by_service == {"api": "v1"}

for field, invalid in (
    ("protocolVersion", "v2"),
    ("acceptedVersions", ["v2"]),
    ("versionByService", {"api": "v2"}),
    ("attempt", "1"),
):
    candidate = dict(payload)
    candidate[field] = invalid
    try:
        Dispatch.model_validate(candidate, strict=True)
    except ValidationError:
        pass
    else:
        raise AssertionError(f"accepted invalid {field}")

value.protocol_version = "v1"
try:
    value.protocol_version = "v2"
except ValidationError:
    pass
else:
    raise AssertionError("assignment accepted an unknown enum value")

def nodes(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from nodes(child)
    elif isinstance(value, list):
        for child in value:
            yield from nodes(child)

assert any(node.get("enum") == ["v1"] for node in nodes(Dispatch.model_json_schema()))
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated enum strict validation failed: %v\n%s", err, out)
	}
}
