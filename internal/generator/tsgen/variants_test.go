package tsgen

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// variantCheck is the file tsc checks fixture-variant-field-db's package
// against, and bun runs (D149): a step's result is typed as the union of
// its members; validateStep holds it to the member its kind names, its
// errors under result, "variant" while the kind names none and "type" for
// a value that is no object; and parseStepFromJSON reads the member the
// kind names, though a review result that also holds passed reads as a
// VerifyResult by its shape.
const variantCheck = `import type { Step, StepResult } from './types';
import { StepKind, Verdict } from './types';
import { parseStepFromJSON, validateStep } from './validators';

const result: StepResult = { verdict: Verdict.Approved };
export const typed: Step = { title: 't', kind: StepKind.Review, result };

function fail(message: string): never {
  throw new Error(message);
}

function rules(value: unknown): string {
  const checked = validateStep(value as Step);
  return checked === true ? '' : JSON.stringify(checked);
}

if (rules({ title: 't', kind: 'verify', result: { passed: true } }) !== '') fail('a verify result is refused');
if (rules({ title: 't', kind: 'note' }) !== '') fail('a note with no result is refused');
if (!rules({ title: 't', kind: 'note', result: { passed: true } }).includes('"variant"')) fail('a note with a result passes');
if (!rules({ title: 't', kind: 'review', result: { passed: true } }).includes('"verdict"')) fail('a review result without its verdict passes');
if (!rules({ title: 't', kind: 'verify', result: 5 }).includes('"type"')) fail('a result that is no object passes');

const review = parseStepFromJSON({ title: 't', kind: 'review', result: { verdict: 'approved', passed: true } });
if (!review.result || !('verdict' in review.result)) fail('the review result is not read as a ReviewResult');
console.log('variant checks passed');
`

// TestVariantFieldPackages generates fixture-variant-field-db's package and
// fixture-variant-field-api's, which names the database's union in its
// views, type-checks both, the database's against variantCheck, and runs
// variantCheck with bun.
func TestVariantFieldPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the variant field's packages in -short mode")
	}
	load := func(service string) *ir.Schema {
		t.Helper()
		schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		return schema
	}
	db := load("fixture-variant-field-db")
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{
		{name: "fixture-variant-field-db", schema: db, extra: map[string]string{"variant.check.ts": variantCheck}},
		{name: "fixture-variant-field-api", schema: load("fixture-variant-field-api"), deps: map[string]*ir.Schema{"fixture-variant-field-db": db}},
	})
	run := exec.Command(bunPath, "run", "variant.check.ts")
	run.Dir = filepath.Join(typesRoot, "fixture-variant-field-db")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("variant.check.ts: %v\n%s", err, out)
	}
}
