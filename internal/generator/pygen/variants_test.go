package pygen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestVariantFieldModels generates fixture-variant-field-db's package and
// runs its models (D149): a step reads its result as the member its kind
// names, though a review result that also holds passed is a VerifyResult
// by its shape; a result that does not read as that member is refused; a
// note reads none; validate_all reports "variant" for a result while the
// kind names no member, and a model that holds another member than the one
// its kind names; and a step dumps as it was read.
func TestVariantFieldModels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-variant-field-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-variant-field-db"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(testpaths.RepoRoot(t), "runtime", "schema", "python")
	command := exec.Command(uv, "run", "--project", project, "python", "-B", "-c", `
import importlib
import sys
from pydantic import ValidationError

module = importlib.import_module(sys.argv[1])
Step, VerifyResult, ReviewResult = module.Step, module.VerifyResult, module.ReviewResult

review = Step.model_validate({"title": "t", "kind": "review", "result": {"verdict": "approved", "passed": True}}, strict=True)
assert isinstance(review.result, ReviewResult), review.result
verify = Step.model_validate({"title": "t", "kind": "verify", "result": {"passed": True}}, strict=True)
assert isinstance(verify.result, VerifyResult), verify.result
note = Step.model_validate({"title": "t", "kind": "note"}, strict=True)
assert note.result is None and not note.validate_all().errors, note
try:
    Step.model_validate({"title": "t", "kind": "verify", "result": {"verdict": "approved"}}, strict=True)
except ValidationError:
    pass
else:
    raise AssertionError("a verify step with a review result decoded")

stray = Step.model_validate({"title": "t", "kind": "note", "result": {"passed": True}}, strict=True)
assert [e["validator"] for e in stray.validate_all().errors["result"]] == ["variant"], stray.validate_all().errors
wrong = Step.model_construct(title="t", kind="review", result=VerifyResult(passed=True))
assert [e["validator"] for e in wrong.validate_all().errors["result"]] == ["variant"], wrong.validate_all().errors
assert review.model_dump(mode="json", by_alias=True, exclude_none=True)["result"] == {"verdict": "approved"}
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated models failed: %v\n%s", err, result)
	}
}
