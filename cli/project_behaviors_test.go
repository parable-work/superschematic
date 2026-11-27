package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// prepareProjectBehaviors copies testdata/project-behaviors, a project
// whose schemas root's superschematic.toml names its behaviors directory
// and whose crates/rules holds the crate those behaviors name, and writes
// the schemas root's tsconfig.base.json over this checkout's packages and
// superscalar, as prepareTSServicesRoot does. It returns the schemas root.
func prepareProjectBehaviors(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("../third_party/superscalar/bindings/typescript/src/index.ts"); err != nil {
		t.Skip("the superscalar checkout (third_party/superscalar) is absent: scripts/superscalar-dep.sh --checkout")
	}
	project := filepath.Join(t.TempDir(), "project")
	copyDir(t, "testdata/project-behaviors", project)
	root := filepath.Join(project, "schemas")
	baseConfig, err := os.ReadFile("../internal/loader/tsreader/testdata/tsconfig.base.json")
	require.NoError(t, err)
	packagesRoot, err := filepath.Abs("../packages")
	require.NoError(t, err)
	scalarsRoot, err := filepath.Abs("../third_party/superscalar")
	require.NoError(t, err)
	text := strings.ReplaceAll(string(baseConfig), "../../../../third_party/superscalar", filepath.ToSlash(scalarsRoot))
	text = strings.ReplaceAll(text, "../../../../packages", filepath.ToSlash(packagesRoot))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tsconfig.base.json"), []byte(text), 0o644))
	return root
}

// A project's own behaviors (D103's step 5): the class of its behaviors
// directory's rating.behavior.ts and the declaration its flag.behavior.yaml
// holds register before the schemas load, so movies-db's Movie implements
// notes.Rating through Behavior<typeof Rating, Movie, C>, and flags-db's
// JSON Post implements notes.Flag by name, as a table implements one of
// the core's, each on a project's core with the crate it names.
func TestAProjectDeclaresItsOwnBehaviors(t *testing.T) {
	root := prepareProjectBehaviors(t)
	out, err := runCheckCommand(t, root)
	require.NoError(t, err, out)
	require.Contains(t, out, "Checked 2 schema services")

	// The declarations the project's behaviors derive are the binary's:
	// `behaviors --extension notes` writes them as it writes an
	// extension's.
	dir := t.TempDir()
	var stdout bytes.Buffer
	cmd := New(Config{})
	cmd.SetOut(&stdout)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"behaviors", "--naming", filepath.Join(root, "superschematic.toml"), "--extension", "notes", "--out", dir})
	require.NoError(t, cmd.Execute(), stdout.String())
	rating, err := os.ReadFile(filepath.Join(dir, "notes.Rating.behavior.json"))
	require.NoError(t, err)
	for _, want := range []string{`"name": "notes.Rating"`, `"name": "rate"`, `"name": "ratings"`, `"key": "rater"`, `"permissions": [`, `"clearPermission"`} {
		require.Contains(t, string(rating), want)
	}
	flag, err := os.ReadFile(filepath.Join(dir, "notes.Flag.behavior.json"))
	require.NoError(t, err)
	require.Contains(t, string(flag), `"name": "notes.Flag"`)
	require.NotContains(t, string(flag), `"crate"`, "the crate is the spec's, not the declaration's")

	// A build of movies-db writes its rules crate: the core with
	// notes.Rating from the project's crates/rules (internal/generator/
	// rulescore).
	out = filepath.Join(t.TempDir(), "dist")
	runCLI(t, "build", filepath.Join(root, "services", "movies-db"), "--out", out, "--skip-format")
	lib, err := os.ReadFile(filepath.Join(out, "rules", "rust", "movies-db", "src", "lib.rs"))
	require.NoError(t, err)
	require.Contains(t, string(lib), ".with(notes_rules::Rating)")
	require.Contains(t, string(lib), ".with(notes_rules::Score)")
	manifest, err := os.ReadFile(filepath.Join(out, "rules", "rust", "movies-db", "Cargo.toml"))
	require.NoError(t, err)
	require.Contains(t, string(manifest), `notes-rules = { path = `)
}

// A config a project's behavior's config schema refuses fails the check,
// as one of the core's does.
func TestAProjectBehaviorsConfigIsHeldToItsSchema(t *testing.T) {
	root := prepareProjectBehaviors(t)
	schema := filepath.Join(root, "services", "movies-db", "src", "movies.schema.ts")
	text, err := os.ReadFile(schema)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(schema, []byte(strings.Replace(string(text), "maxStars: 5", "maxStars: 50", 1)), 0o644))
	out, err := runCheckCommand(t, root)
	require.Error(t, err, out)
	require.Contains(t, out+err.Error(), "maxStars")
}
