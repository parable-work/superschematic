package cli

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/registry"
)

// TestCoreOnlyBinaryBuildsAFixture pins that New with no extensions, which
// is what cmd/superschematic runs, builds a fixture end to end under the
// default naming.
func TestCoreOnlyBinaryBuildsAFixture(t *testing.T) {
	out := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-db"), "--out", out})

	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "Loaded schema fixture-db (kind DB)")
	assert.DirExists(t, filepath.Join(out, "types", "go", "fixture-db"))
}

func TestNewNamesTheBinaryFromConfig(t *testing.T) {
	root := New(Config{Name: "acme-schemas", Short: "Acme's schema build"})
	assert.Equal(t, "acme-schemas", root.Use)
	assert.Equal(t, "Acme's schema build", root.Short)
	assert.Contains(t, root.Long, "acme-schemas reads schema service directories")

	core := New(Config{})
	assert.Equal(t, "psgen", core.Use)
	assert.NotContains(t, core.Short, "Parable")
	assert.NotContains(t, core.Long, "Parable")
}

// commandExtension registers nothing and contributes one subcommand.
type commandExtension struct{}

func (commandExtension) Name() string                      { return "test-commands" }
func (commandExtension) Register(*registry.Registry) error { return nil }
func (commandExtension) Commands() []*cobra.Command {
	return []*cobra.Command{{Use: "hello", RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := cmd.OutOrStdout().Write([]byte("hello from the extension\n"))
		return err
	}}}
}

func TestNewAddsTheSubcommandsOfACommandProvider(t *testing.T) {
	core := New(Config{})
	for _, cmd := range core.Commands() {
		assert.NotEqual(t, "hello", cmd.Name(), "core-only root carries an extension subcommand")
	}

	buf := new(bytes.Buffer)
	root := New(Config{}, commandExtension{})
	root.SetOut(buf)
	root.SetArgs([]string{"hello"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "hello from the extension\n", buf.String())
}
