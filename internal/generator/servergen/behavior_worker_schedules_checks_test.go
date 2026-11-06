package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
)

// TestATypeScriptImplementationThatPredatesItsEventHandlersFails (D104):
// fixture-schedules-api's behavior worker ProblemWakeUps runs Problem's
// event queue, whose run events the implementation's Workers method
// problemWakeUpsSchedulesRun handles, so its worker's main calls
// createWorkers as a D80 worker's does. An implementation that exists, and
// so is the engineer's, and exports no createWorkers fails the build, which
// writes nothing into it and says what to add, the event's method among
// it; with createWorkers exported, it builds.
func TestATypeScriptImplementationThatPredatesItsEventHandlersFails(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptSchedulesFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tsSchedulesOrder...)
	dir := naming.Default().TypeScriptImplementationDir(repoRoot, tsSchedulesAPI)
	file := filepath.Join(dir, tsrestgen.ImplementationFile)
	build := func(code string) error {
		t.Helper()
		if err := os.WriteFile(file, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := generator.Run(f.schemas[tsSchedulesStack], f.configs[tsSchedulesStack], f.options(repoRoot, tsBehaviorsFakePaths(repoRoot)))
		if got, rerr := os.ReadFile(file); rerr != nil || string(got) != code {
			t.Errorf("a build wrote %s: %q, %v", file, got, rerr)
		}
		return err
	}
	before := "// The engineer's code, from before the event's handler.\nexport const create = () => ({});\nexport const authenticate = () => async () => null;\n"
	err := build(before)
	for _, want := range []string{
		"stack " + tsSchedulesStack + ": " + tsSchedulesAPI + " declares workers, and its implementation at " + dir + ", which the build no longer writes into, exports no createWorkers",
		"export const createWorkers: WorkersConstructor = deps => ({",
		"async problemWakeUpsSchedulesRun(event, tx, signal) {\n      // handle the event run of Problem's Schedules\n    },",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if err := build(before + "export const createWorkers = () => ({});\n"); err != nil {
		t.Fatalf("a build over an implementation that exports createWorkers = %v", err)
	}
}
