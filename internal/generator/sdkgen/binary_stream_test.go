package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
)

const streamService = "fixture-stream-api"

// TestBinaryStreamSDKCompilesAndRuns type-checks the TypeScript SDK of
// fixture-stream-api against its generated types package, then runs
// test_binary_stream.js: the SDK's exportReport, an @httpBinaryStream
// operation (D167), against a server that streams an Arrow IPC body in
// chunks with a header. The method resolves to the stream unread, a
// refusal is the ApiError of the problem, and a cancel before the end
// ends the request.
func TestBinaryStreamSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, streamService))
	if err != nil {
		t.Fatalf("load %s: %v", streamService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: streamService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: streamService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesDir := writeTypesPackage(t, bunPath, schema, streamService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", streamService)
	writePagedSDK(t, apiOutput, tsgen.ParseableTypeNames(tsOutput), sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_binary_stream.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("test_binary_stream.js failed: %v\n%s", err, out)
	}
	t.Logf("test_binary_stream.js:\n%s", out)
}
