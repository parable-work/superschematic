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
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
)

// TestIdempotentOperationsSendOneKeyACall type-checks the TypeScript SDK
// of fixture-idempotent-api against its generated types package, then runs
// test_idempotency.js (D140): a method of an @idempotent operation sends
// an Idempotency-Key, its options' idempotencyKey or a UUID it makes for
// each call; a method of any other operation sends none.
func TestIdempotentOperationsSendOneKeyACall(t *testing.T) {
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
	schema, table, err := sdktest.LoadIdempotentService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.IdempotentService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:         sessionauth.Provider{},
		SchemaName:       sdktest.IdempotentService,
		Clock:            nestedArraysClock,
		IdempotencyTable: table,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: sdktest.IdempotentService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesDir := writeTypesPackage(t, bunPath, schema, sdktest.IdempotentService, tempRoot)
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", sdktest.IdempotentService)
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_idempotency.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("idempotency SDK runtime test failed: %v\n%s", err, out)
	}
}
