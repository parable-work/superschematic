package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/stretchr/testify/require"
)

// TestServiceCredentialRuntime_TypeScript runs test_service_credential.js
// on the HttpClient of fixture-api's SDK, which has end-user auth (D37):
// every request carries the service credential in each configured header;
// a 401 with the code service_unauthorized, in an RFC 9457 problem or the
// Rust envelope, asks the source for a fresh token once and never refreshes
// the end user; any other 401 refreshes the end user and never asks for a
// fresh service token; a call spends at most one of each. A forwarded end
// user replaces the configured token and is never refreshed.
func TestServiceCredentialRuntime_TypeScript(t *testing.T) {
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun is required for the TypeScript service credential runtime test: %v", err))
	}

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve current test file path")

	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "resolve temp dir")

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	_, apiOutput, parseable := loadFixtureAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, fixedClock)
	require.NoError(t, err)
	require.True(t, sdkOutput.HasAuth, "fixture-api's SDK has end-user auth")

	sdkDir := filepath.Join(tempRoot, "sdk")
	require.NoError(t, WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, fixedClock))

	// No bun install: the generated client only imports ./types.
	cmd := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_service_credential.js"))
	cmd.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "TypeScript service credential runtime test failed:\n%s", string(output))
}

// TestRequestOptionsRuntime_TypeScript type-checks the SDK of
// query-lists-api and runs test_request_options.js: a generated method's
// last argument is still an AbortSignal, or RequestOptions with a signal
// and an end user to forward, as a server's RequestContext.
func TestRequestOptionsRuntime_TypeScript(t *testing.T) {
	runInQueryListsSDK(t, "test_request_options.js")
}
