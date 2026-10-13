package stackinit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stackinit"
)

// project lays out a project at a temporary directory: a schemas root,
// and files by path. A repository is made with git init when repo is set.
func project(t *testing.T, repo bool, files map[string]string) (root, schemasRoot string) {
	t.Helper()
	root = t.TempDir()
	schemasRoot = filepath.Join(root, "schemas")
	require.NoError(t, os.MkdirAll(schemasRoot, 0o755))
	if repo {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
		cmd := exec.Command("git", "init", "-q", ".")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return root, schemasRoot
}

// ignored asks git whether it ignores path, relative to dir, by its rules.
func ignored(t *testing.T, dir, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", "--", path)
	cmd.Dir = dir
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false
	}
	require.NoError(t, err)
	return true
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const comment = "# The output root is generated, but for its Bun workspace's lockfile, which the\n# project commits (D51); .superschematic holds this machine's local secrets and keys.\n"

// TestIgnoreRulesInANewFile: with no .gitignore over the schemas root, the
// schemas root gets one, and a second run adds nothing.
func TestIgnoreRulesInANewFile(t *testing.T) {
	_, schemasRoot := project(t, false, nil)
	edit, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	file := filepath.Join(schemasRoot, ".gitignore")
	assert.Equal(t, file, edit.File)
	assert.True(t, edit.Created)
	assert.Equal(t, []string{"/dist/*", "!/dist/bun.lock", "/.superschematic/"}, edit.Added)
	assert.Equal(t, comment+"/dist/*\n!/dist/bun.lock\n/.superschematic/\n", read(t, file))

	again, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.False(t, again.Changed())
	assert.False(t, again.Created)
	assert.Equal(t, comment+"/dist/*\n!/dist/bun.lock\n/.superschematic/\n", read(t, file))
}

// TestIgnoreRulesTakeTheOutputRootBack: in a repository whose root ignores
// every dist directory, the root's .gitignore is the one that covers the
// schemas root. Its rules name the schemas root, and since git cannot take
// a file back from an ignored directory, the directory is taken back after
// the broader rule. Every other dist stays ignored, and a second run adds
// nothing.
func TestIgnoreRulesTakeTheOutputRootBack(t *testing.T) {
	root, schemasRoot := project(t, true, map[string]string{".gitignore": "node_modules/\ndist/\n"})
	edit, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".gitignore"), edit.File)
	assert.False(t, edit.Created)
	assert.Equal(t, []string{"/schemas/dist/*", "!/schemas/dist/bun.lock", "/schemas/.superschematic/", "!/schemas/dist/"}, edit.Added)
	assert.Empty(t, edit.StillIgnored)
	assert.Equal(t, "node_modules/\ndist/\n\n"+comment+"/schemas/dist/*\n!/schemas/dist/bun.lock\n/schemas/.superschematic/\n!/schemas/dist/\n", read(t, edit.File))

	assert.False(t, ignored(t, schemasRoot, "dist/bun.lock"))
	assert.True(t, ignored(t, schemasRoot, "dist/server/notes-stack/main.go"))
	assert.True(t, ignored(t, schemasRoot, ".superschematic/local/notes-stack/Dev/secrets.env"))
	assert.True(t, ignored(t, root, "web/dist/index.html"))

	again, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.False(t, again.Changed())
	assert.Empty(t, again.StillIgnored)
}

// TestIgnoreRulesRewriteTheDirectoryRule: a rule on the output root itself
// in the schemas root's own .gitignore becomes the rule on its contents, in
// place, and the rules it already has are kept.
func TestIgnoreRulesRewriteTheDirectoryRule(t *testing.T) {
	_, schemasRoot := project(t, true, map[string]string{
		"schemas/.gitignore": "/dist/\n.superschematic\n*.log\n",
	})
	edit, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(schemasRoot, ".gitignore"), edit.File)
	assert.Equal(t, map[string]string{"/dist/": "/dist/*"}, edit.Replaced)
	assert.Equal(t, []string{"!/dist/bun.lock"}, edit.Added)
	assert.Equal(t, "/dist/*\n.superschematic\n*.log\n\n"+comment+"!/dist/bun.lock\n", read(t, edit.File))
	assert.False(t, ignored(t, schemasRoot, "dist/bun.lock"))
	assert.True(t, ignored(t, schemasRoot, "dist/stack/notes-stack/Dev/environment.json"))

	again, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.False(t, again.Changed())
}

// TestIgnoreRulesNameARuleTheyCannotOverrule: a rule of the project's that
// ignores the lockfile itself, which taking the output root back does not
// change, is named rather than edited, and the file stays as it was.
func TestIgnoreRulesNameARuleTheyCannotOverrule(t *testing.T) {
	const rules = "/dist/*\n!/dist/bun.lock\n/.superschematic/\n"
	_, schemasRoot := project(t, true, map[string]string{
		"schemas/.gitignore":      rules,
		"schemas/dist/.gitignore": "bun.lock\n",
	})
	edit, err := stackinit.EnsureIgnoreRules(schemasRoot)
	require.NoError(t, err)
	assert.False(t, edit.Changed())
	assert.Equal(t, "schemas/dist/.gitignore:1:bun.lock", edit.StillIgnored)
	assert.Equal(t, rules, read(t, edit.File))
}
