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

// encryptedArgumentAPI is apigen's TypeScript schema with an
// EncryptedField<string> argument outside an Encrypted operation set.
const encryptedArgumentAPI = "encrypted-argument-api"

// TestEncryptedArgumentSDKCompilesAndRuns type-checks the TypeScript SDK of
// encrypted-argument-api against its generated types package, then runs
// test_encrypted_argument.js: storeCard, which takes an
// EncryptedField<string> argument, sends its body as an RSA-OAEP envelope
// the private key opens to the arguments, and renameCard sends plain JSON.
func TestEncryptedArgumentSDKCompilesAndRuns(t *testing.T) {
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

	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", encryptedArgumentAPI))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedArgumentAPI, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: encryptedArgumentAPI,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: encryptedArgumentAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	typesDir := writeTypesPackage(t, bunPath, schema, encryptedArgumentAPI, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", encryptedArgumentAPI)
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
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_encrypted_argument.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("encrypted argument SDK runtime test failed: %v\n%s", err, out)
	}
}
