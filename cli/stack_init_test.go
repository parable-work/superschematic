package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// initFixture is a tree with no stack: notes-api, which end users call and
// which calls notes-index, whose every operation is @requireService, their
// database notes-db, and the site notes-web, which calls notes-api.
const initFixture = "../internal/stackinit/testdata/tree"

// prepareInitProject copies the fixture into a project of its own, its
// schemas root at schemas/ with the base tsconfig's paths made absolute,
// makes the project the working directory, and returns the project's
// directory.
func prepareInitProject(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	copyDir(t, filepath.Join(initFixture, "services"), filepath.Join(schemasRoot, "services"))
	base, err := os.ReadFile(filepath.Join(initFixture, "tsconfig.base.json"))
	require.NoError(t, err)
	text := strings.ReplaceAll(string(base), "../../../../", filepath.ToSlash(repoRoot)+"/")
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, "tsconfig.base.json"), []byte(text), 0o644))
	t.Chdir(project)
	return project
}

// dropSite takes the site out of the project. On stacktest's fake target a
// site with no domain is reached at an output of its own resource, so the
// CORS of the server it calls waits on the site, which rolls out after
// that server, and the environment does not resolve; gcp's test, in
// cmd/superschematic, resolves the whole tree.
func dropSite(t *testing.T, project string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(filepath.Join(project, "schemas", "services", "notes-web")))
}

// runStackInitCmd runs `stack init` with args in a binary that links exts.
func runStackInitCmd(t *testing.T, exts []registry.Extension, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{}, exts...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"stack", "init"}, args...))
	err := root.Execute()
	return buf.String(), err
}

// withFakeCloud makes stacktest's fake target the one init writes the
// cloud environment on, in gcp's place, which the root module does not
// link.
func withFakeCloud(t *testing.T) []registry.Extension {
	t.Helper()
	prev := cloudTarget
	cloudTarget = stacktest.Target
	t.Cleanup(func() { cloudTarget = prev })
	return []registry.Extension{&stacktest.Extension{}}
}

// buildStack builds the stack service alone, as stack dev does last, and
// returns the output root, where the stack generator wrote each
// environment it resolved.
func buildStack(t *testing.T, exts []registry.Extension, dir string) string {
	t.Helper()
	out := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{}, exts...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"build", dir, "--out", out})
	require.NoError(t, root.Execute(), buf.String())
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestStackInitWithNoFlags: without a terminal and with no flag, init
// writes a stack named after the tree with Dev alone, which loads and
// resolves, adds the ignore rules, and names the next command.
func TestStackInitWithNoFlags(t *testing.T) {
	project := prepareInitProject(t)
	out, err := runStackInitCmd(t, nil)
	require.NoError(t, err, out)

	dir := filepath.Join(project, "schemas", "services", "notes-stack")
	for _, f := range []string{"package.json", "tsconfig.json", "schema.config.ts", "src/stack.schema.ts"} {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(f)))
	}
	assert.NoFileExists(t, filepath.Join(dir, "src", "gcp-target.ts"))
	schema := readFile(t, filepath.Join(dir, "src", "stack.schema.ts"))
	assert.Contains(t, schema, `@stack({ deploy: [NotesApi, NotesIndex, NotesWeb], expose: [NotesApi] })`)
	assert.Contains(t, schema, "@environment({ target: \"local\" })\nexport abstract class Dev {}\n")
	assert.Contains(t, readFile(t, filepath.Join(dir, "schema.config.ts")), "outputs: {}")
	assert.Equal(t, "/dist/*\n!/dist/bun.lock\n/.superschematic/\n", lastLines(readFile(t, filepath.Join(project, "schemas", ".gitignore")), 3))

	assert.Contains(t, out, "Wrote the Stack service notes-stack in ./schemas/services/notes-stack:\n")
	assert.Contains(t, out, "  notes-api    exposed: NoteQueries.getNote is not @requireService\n")
	assert.Contains(t, out, "  notes-index  internal: every operation is @requireService\n")
	assert.Contains(t, out, "  A site is always exposed: notes-web.\n")
	assert.Contains(t, out, "Created ./schemas/.gitignore: added /dist/*, !/dist/bun.lock, /.superschematic/.\n")
	assert.NotContains(t, out, "does not resolve")
	assert.True(t, strings.HasSuffix(out, "\nNext:\n  superschematic stack dev --environment Dev\n"), out)

	built := buildStack(t, nil, dir)
	assert.FileExists(t, stack.EnvironmentPath(built, "notes-stack", "Dev"))
}

// lastLines returns the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}

// TestStackInitTakesFlags: every value is a flag. The cloud environment
// gets the name, project and region given and the target's types, CI is
// on, --expose replaces the derived list, and the stack resolves in both
// environments. The stack under its own name is not the one the stack
// commands find in the working directory, so the next commands name it.
func TestStackInitTakesFlags(t *testing.T) {
	exts := withFakeCloud(t)
	project := prepareInitProject(t)
	dropSite(t, project)
	t.Chdir(filepath.Join(project, "schemas"))
	out, err := runStackInitCmd(t, exts, "services", "--name", "notes", "--gcp-project", "notes-staging", "--region", "us-east1",
		"--environment", "Preprod", "--ci", "github", "--expose", "notes-index,notes-api")
	require.NoError(t, err, out)

	dir := filepath.Join(project, "schemas", "services", "notes")
	schema := readFile(t, filepath.Join(dir, "src", "stack.schema.ts"))
	assert.Contains(t, schema, "// It exposes the APIs it was told to; only other servers reach the rest.\n@stack({ deploy: [NotesApi, NotesIndex], expose: [NotesApi, NotesIndex] })")
	assert.Contains(t, schema, "export abstract class Notes {}\n")
	assert.Contains(t, schema, `@environment({ target: "fake", fake: { project: "notes-staging", region: "us-east1" } })`+"\nexport abstract class Preprod {}\n")
	assert.Contains(t, readFile(t, filepath.Join(dir, "src", "fake-target.ts")), "declare module \"@superschematic/stack\" {\n  interface Targets {\n    fake: {\n")
	assert.Contains(t, readFile(t, filepath.Join(dir, "schema.config.ts")), "outputs: { ci: { github: {} } }")

	assert.Contains(t, out, "  notes-api    exposed: --expose names it\n")
	assert.Contains(t, out, "  notes-index  exposed: --expose names it\n")
	assert.Contains(t, out, "  Preprod on fake, project notes-staging in us-east1\n")
	assert.Contains(t, out, "CI: outputs.ci writes the github workflow on each build of the stack.\n")
	assert.NotContains(t, out, "does not resolve")
	assert.True(t, strings.HasSuffix(out, "\nNext:\n"+
		"  superschematic stack dev ./services/notes --environment Dev\n"+
		"  superschematic stack bootstrap Preprod --stack ./services/notes\n"), out)

	built := buildStack(t, exts, dir)
	for _, env := range []string{"Dev", "Preprod"} {
		assert.FileExists(t, stack.EnvironmentPath(built, "notes", env))
	}
}

// TestStackInitWithoutATerminal: a cloud environment needs its project and
// region together, and with no terminal to ask, either alone is an error.
func TestStackInitWithoutATerminal(t *testing.T) {
	exts := withFakeCloud(t)
	for _, args := range [][]string{
		{"--gcp-project", "notes-staging"},
		{"--region", "us-east1"},
		{"--environment", "Preprod"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			project := prepareInitProject(t)
			_, err := runStackInitCmd(t, exts, args...)
			require.EqualError(t, err, "a gcp environment needs --gcp-project and --region together")
			assert.NoDirExists(t, filepath.Join(project, "schemas", "services", "notes-stack"))
		})
	}
}

// TestStackInitRefuses: a tree with no API, a stack already there, a gcp
// environment from a binary without the gcp target, and values that do
// not hold.
func TestStackInitRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cloud bool
		setup func(t *testing.T, project string)
		args  []string
		want  string
	}{
		{
			name: "no API",
			setup: func(t *testing.T, project string) {
				for _, svc := range []string{"notes-api", "notes-index", "notes-web"} {
					require.NoError(t, os.RemoveAll(filepath.Join(project, "schemas", "services", svc)))
				}
			},
			want: "no API service under",
		},
		{
			name: "a stack already",
			setup: func(t *testing.T, project string) {
				_, err := runStackInitCmd(t, nil)
				require.NoError(t, err)
			},
			want: "the tree has a Stack service already, notes-stack (./schemas/services/notes-stack); pass --force",
		},
		{
			name: "the gcp target unlinked",
			args: []string{"--gcp-project", "notes-staging", "--region", "us-east1"},
			want: "a gcp environment needs the gcp target, which this binary does not link (its targets: local); the installed superschematic links it",
		},
		{name: "a bad name", args: []string{"--name", "Notes"}, want: `stack name "Notes": want lower-case letters and digits`},
		{name: "a service's name", args: []string{"--name", "notes-api"}, want: "./schemas/services/notes-api is the API service notes-api; name the stack another way"},
		{name: "an unknown renderer", args: []string{"--ci", "jenkins"}, want: "--ci jenkins: no CI renderer of that name (registered: github)"},
		{name: "a site's API left out", args: []string{"--expose", "none"}, want: "site notes-web calls notes-api, so notes-api must be exposed (D55)"},
		{name: "an unknown API", args: []string{"--expose", "notes-api,notes-db"}, want: "expose names notes-db, which is no API or Site service of the tree"},
		{
			name:  "the local environment's name",
			cloud: true,
			args:  []string{"--gcp-project", "notes-staging", "--region", "us-east1", "--environment", "Dev"},
			want:  "environment name Dev is taken",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var exts []registry.Extension
			if tc.cloud {
				exts = withFakeCloud(t)
			}
			project := prepareInitProject(t)
			if tc.setup != nil {
				tc.setup(t, project)
			}
			out, err := runStackInitCmd(t, exts, tc.args...)
			require.Error(t, err, out)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestStackInitForce: --force replaces the stack of the name it is given,
// its config and src, keeping the ignore rules as they are; under another
// name it writes a second stack, which the stack commands then need named.
func TestStackInitForce(t *testing.T) {
	project := prepareInitProject(t)
	ignore := filepath.Join(project, "schemas", ".gitignore")
	_, err := runStackInitCmd(t, nil)
	require.NoError(t, err)
	rules := readFile(t, ignore)
	dir := filepath.Join(project, "schemas", "services", "notes-stack")
	stale := filepath.Join(dir, "src", "old.schema.ts")
	require.NoError(t, os.WriteFile(stale, []byte("export {};\n"), 0o644))

	out, err := runStackInitCmd(t, nil, "--force", "--ci", "github")
	require.NoError(t, err, out)
	assert.NoFileExists(t, stale)
	assert.Contains(t, readFile(t, filepath.Join(dir, "schema.config.ts")), "outputs: { ci: { github: {} } }")
	assert.Contains(t, out, "./schemas/.gitignore has the ignore rules already.\n")
	assert.Equal(t, rules, readFile(t, ignore))
	assert.True(t, strings.HasSuffix(out, "\nNext:\n  superschematic stack dev --environment Dev\n"), out)

	out, err = runStackInitCmd(t, nil, "--force", "--name", "notes-stack-two")
	require.NoError(t, err, out)
	assert.FileExists(t, filepath.Join(project, "schemas", "services", "notes-stack-two", "src", "stack.schema.ts"))
	assert.FileExists(t, filepath.Join(dir, "src", "stack.schema.ts"))
	assert.Contains(t, out, "Next:\n  superschematic stack dev ./schemas/services/notes-stack-two --environment Dev\n")
}

// TestStackInitIgnoreRulesInARepository: in a repository whose root
// ignores every dist, init edits the root's .gitignore, the one that
// covers the schemas root, so git keeps the lockfile, and a second run
// adds nothing.
func TestStackInitIgnoreRulesInARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	project := prepareInitProject(t)
	git := exec.Command("git", "init", "-q", ".")
	git.Dir = project
	require.NoError(t, git.Run())
	ignore := filepath.Join(project, ".gitignore")
	require.NoError(t, os.WriteFile(ignore, []byte("dist/\n"), 0o644))

	out, err := runStackInitCmd(t, nil)
	require.NoError(t, err, out)
	assert.Contains(t, out, "In ./.gitignore: added /schemas/dist/*, !/schemas/dist/bun.lock, /schemas/.superschematic/, !/schemas/dist/.\n")
	rules := readFile(t, ignore)
	assert.True(t, strings.HasPrefix(rules, "dist/\n\n# "), rules)
	assert.Equal(t, "/schemas/dist/*\n!/schemas/dist/bun.lock\n/schemas/.superschematic/\n!/schemas/dist/\n", lastLines(rules, 4))
	assert.NoFileExists(t, filepath.Join(project, "schemas", ".gitignore"))

	out, err = runStackInitCmd(t, nil, "--force")
	require.NoError(t, err, out)
	assert.Contains(t, out, "./.gitignore has the ignore rules already.\n")
	assert.Equal(t, rules, readFile(t, ignore))
}

// scriptedPrompter answers each prompt with the next line of a script,
// and records the prompts and what it is told.
type scriptedPrompter struct {
	answers []string
	prompts []string
	said    []string
}

func (s *scriptedPrompter) Line(prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if len(s.answers) == 0 {
		return "", io.EOF
	}
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer, nil
}

func (s *scriptedPrompter) Say(line string) { s.said = append(s.said, line) }

// withTerminal makes init ask p, and gcloud's configuration name project
// and region.
func withTerminal(t *testing.T, p *scriptedPrompter, gcloud map[string]string) {
	t.Helper()
	prevPrompter, prevGcloud := newLinePrompter, readGcloudConfig
	newLinePrompter = func(*cobra.Command) linePrompter { return p }
	readGcloudConfig = func(_ context.Context, property string) string { return gcloud[property] }
	t.Cleanup(func() { newLinePrompter, readGcloudConfig = prevPrompter, prevGcloud })
}

// TestStackInitAsks: at a terminal, init asks for each value no flag
// gives, with the tree's defaults and gcloud's project and region, and
// asks again after an answer that does not hold.
func TestStackInitAsks(t *testing.T) {
	exts := withFakeCloud(t)
	project := prepareInitProject(t)
	p := &scriptedPrompter{answers: []string{
		"Notes Stack", // refused: no service name
		"notes-api",   // refused: an API has it
		"",            // notes-stack
		"",            // yes: gcloud names a project
		"Prod",        // the environment
		"",            // gcloud's project
		"",            // gcloud's run/region
		"y",           // CI
		"notes-index", // refused: notes-web calls notes-api
		"",            // the derived list
	}}
	withTerminal(t, p, map[string]string{"project": "notes-staging", "run/region": "us-central1", "compute/region": "europe-west1"})
	out, err := runStackInitCmd(t, exts)
	require.NoError(t, err, out)

	assert.Equal(t, []string{
		"Stack service name [notes-stack]: ",
		"Stack service name [notes-stack]: ",
		"Stack service name [notes-stack]: ",
		"Add an environment on Google Cloud (the gcp target)? [Y/n]: ",
		"Its environment name [Staging]: ",
		"Google Cloud project [notes-staging]: ",
		"Region [us-central1]: ",
		"Write a GitHub Actions workflow for the stack (outputs.ci)? [y/N]: ",
		"APIs to expose, comma-separated, or none [notes-api]: ",
		"APIs to expose, comma-separated, or none [notes-api]: ",
	}, p.prompts)
	assert.Equal(t, []string{
		`stack name "Notes Stack": want lower-case letters and digits in words joined by hyphens, such as shop-stack`,
		"./schemas/services/notes-api is the API service notes-api; name the stack another way",
		"site notes-web calls notes-api, so notes-api must be exposed (D55)",
	}, p.said)

	dir := filepath.Join(project, "schemas", "services", "notes-stack")
	schema := readFile(t, filepath.Join(dir, "src", "stack.schema.ts"))
	assert.Contains(t, schema, `@stack({ deploy: [NotesApi, NotesIndex, NotesWeb], expose: [NotesApi] })`)
	assert.Contains(t, schema, `@environment({ target: "fake", fake: { project: "notes-staging", region: "us-central1" } })`+"\nexport abstract class Prod {}\n")
	assert.Contains(t, readFile(t, filepath.Join(dir, "schema.config.ts")), "outputs: { ci: { github: {} } }")
	assert.True(t, strings.HasSuffix(out, "\nNext:\n  superschematic stack dev --environment Dev\n  superschematic stack bootstrap Prod\n"), out)
}

// TestStackInitAsksNothingTheFlagsGive: with every value a flag, a
// terminal is asked nothing, and gcloud is not read. A "no" leaves the
// cloud environment out.
func TestStackInitAsksNothingTheFlagsGive(t *testing.T) {
	exts := withFakeCloud(t)
	prepareInitProject(t)
	p := &scriptedPrompter{}
	withTerminal(t, p, nil)
	readGcloudConfig = func(context.Context, string) string {
		t.Error("gcloud was read")
		return ""
	}
	out, err := runStackInitCmd(t, exts, "--name", "notes", "--gcp-project", "notes-staging", "--region", "us-east1", "--environment", "Staging", "--ci", "github", "--expose", "notes-api")
	require.NoError(t, err, out)
	assert.Empty(t, p.prompts)

	p2 := &scriptedPrompter{answers: []string{"n"}}
	withTerminal(t, p2, map[string]string{})
	out, err = runStackInitCmd(t, exts, "--force", "--name", "notes", "--ci", "github", "--expose", "notes-api")
	require.NoError(t, err, out)
	assert.Equal(t, []string{"Add an environment on Google Cloud (the gcp target)? [y/N]: "}, p2.prompts)
	assert.NotContains(t, out, "bootstrap")
}

// TestReadGcloudConfig: init reads gcloud's configuration with no stdin and
// a deadline, so a gcloud that never answers costs the deadline and no
// more, and "(unset)" is no value.
func TestReadGcloudConfig(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$3" in
  project) echo notes-staging ;;
  compute/region) echo "(unset)" >&2 ;;
  run/region) sleep 30 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gcloud"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := gcloudTimeout
	gcloudTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gcloudTimeout = prev })

	ctx := context.Background()
	assert.Equal(t, "notes-staging", readGcloudConfig(ctx, "project"))
	assert.Equal(t, "", readGcloudConfig(ctx, "compute/region"))
	start := time.Now()
	assert.Equal(t, "", readGcloudConfig(ctx, "run/region"))
	assert.Less(t, time.Since(start), 5*time.Second)

	defaults := startGcloudDefaults(ctx)()
	assert.Equal(t, gcloudDefaults{project: "notes-staging"}, defaults)

	t.Setenv("PATH", t.TempDir())
	assert.Equal(t, "", readGcloudConfig(ctx, "project"))
}
