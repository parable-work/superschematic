package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/sentinel"
	ir "github.com/parable-work/superschematic/ir"
)

const tsreaderTestdata = "../internal/loader/tsreader/testdata/services"

func TestBuildCommand_MissingDir(t *testing.T) {
	root := New(Config{})
	root.SetArgs([]string{"build", "/nonexistent/service"})

	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "service directory not found")
}

func TestBuildCommand_Summary(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-db"), "--out", t.TempDir()})

	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Loaded schema fixture-db (kind DB)")
	assert.Contains(t, buf.String(), "types-go written to")
}

func TestBuildCommand_Profile(t *testing.T) {
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-db"), "--out", t.TempDir(), "--profile"})

	err := root.Execute()
	require.NoError(t, err)

	assert.Contains(t, out.String(), "Loaded schema fixture-db (kind DB)")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=build.load duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=tsreader.program duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=tsreader.program.host duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=tsreader.program.config duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=tsreader.program.create duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=tsreader.program.source-classify duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.output.types-go duration_ms=")
}

func TestBuildCommand_EmitIR(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-db"), "--emit-ir"})

	err := root.Execute()
	require.NoError(t, err)

	var ir map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &ir))
	assert.Equal(t, "fixture-db", ir["name"])
	assert.Equal(t, "DB", ir["kind"])

}

func TestBuildCommand_EmitsSentinel(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-db"), "--out", t.TempDir()})

	err := root.Execute()
	require.NoError(t, err)
	// The fixture's sentinel is committed; emission must agree byte-for-byte.
	assert.Contains(t, buf.String(), "sentinel up to date")

	data, err := os.ReadFile(filepath.Join(tsreaderTestdata, "fixture-db", "src", "service.generated.ts"))
	require.NoError(t, err)
	assert.Equal(t, sentinel.Content("fixture-db", ir.SchemaKindDB, naming.Default()), string(data))
}

func TestBuildCommand_NoSentinelWithoutTSConfig(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	dataService := "../internal/loader/testdata/services/fixture-db-json"
	root.SetArgs([]string{"build", dataService, "--out", t.TempDir()})

	err := root.Execute()
	require.NoError(t, err)
	// Data-form services without a tsconfig.json have no TypeScript module
	// surface to import a sentinel through, so none is emitted.
	assert.NoFileExists(t, filepath.Join(dataService, "src", "service.generated.ts"))
	assert.NoFileExists(t, filepath.Join(dataService, "src", "index.ts"))
}

// TestBuildCommand_WithDepsBuildsClosureInOrder: one `build --with-deps
// <root>` builds the root's transitive dependencies (here fixture-db,
// reached only through authDb) before the root, and leaves unrelated
// siblings (fixture-general) alone.
func TestBuildCommand_WithDepsBuildsClosureInOrder(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db", "fixture-api", "fixture-general")
	outDir := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build", "--with-deps", filepath.Join(servicesRoot, "fixture-api"), "--out", outDir})

	require.NoError(t, root.Execute())

	out := buf.String()
	assert.Contains(t, out, "Resolved 2 schema services for fixture-api: fixture-db, fixture-api")
	dbBuilt := strings.Index(out, "OK: fixture-db (built)")
	apiBuilt := strings.Index(out, "OK: fixture-api (built)")
	require.Positive(t, dbBuilt, "fixture-db must be built:\n%s", out)
	require.Positive(t, apiBuilt, "fixture-api must be built:\n%s", out)
	assert.Less(t, dbBuilt, apiBuilt, "the authDb builds before the API that depends on it")
	assert.NotContains(t, out, "fixture-general", "siblings outside the closure are not built")
	assert.Contains(t, out, "Built 2 schema services for fixture-api")

	assert.DirExists(t, filepath.Join(outDir, "types", "go", "fixture-db"))
	assert.DirExists(t, filepath.Join(outDir, "api", "fixture-api"))
	assert.NoDirExists(t, filepath.Join(outDir, "types", "typescript", "fixture-general"))
	assert.NoDirExists(t, filepath.Join(outDir, "types", "python", "fixture-general"))
}

func TestBuildCommand_WithDepsRejectsEmitIR(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db")
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetArgs([]string{"build", "--with-deps", "--emit-ir", filepath.Join(servicesRoot, "fixture-db")})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--emit-ir")
	assert.Contains(t, err.Error(), "--with-deps")
}

func TestBuildCommand_SchemaError(t *testing.T) {
	root := New(Config{})
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "broken-illegal-import")})

	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot import @superschematic/db")
}
