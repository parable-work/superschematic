package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// subjectTypesSource is the core's list of the IR types of a field that
// holds a subject, which a Workflow transition's by may name (D104).
const subjectTypesSource = "runtime/engine/rust/rules-behaviors/src/subjects.rs"

// TestSubjectTypesAreTheCores (D104): the build's subjectTypes are the
// core's SUBJECT_TYPES, in order, so the build refuses the by fields the
// core refuses at a command, in the same words, and accepts the rest.
func TestSubjectTypesAreTheCores(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), filepath.FromSlash(subjectTypesSource)))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`pub const SUBJECT_TYPES: \[&str; \d+\] = \[([^\]]*)\];`).FindSubmatch(data)
	if match == nil {
		t.Fatalf("%s declares no SUBJECT_TYPES", subjectTypesSource)
	}
	var core []string
	for _, quoted := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(match[1], -1) {
		core = append(core, string(quoted[1]))
	}
	if !slices.Equal(subjectTypes, core) {
		t.Errorf("subjectTypes = %v, the core's SUBJECT_TYPES = %v (%s)", subjectTypes, core, subjectTypesSource)
	}
}
