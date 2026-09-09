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

func TestStrictEnvelopeUnwrapRuntime_TypeScript(t *testing.T) {
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun is required for the TypeScript runtime envelope test: %v", err))
	}

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve current test file path")

	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "resolve temp dir")

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	_, apiOutput, parseable := loadFixtureAPI(t)

	sdkOutput, err := Generate(apiOutput, parseable, fixedClock)
	require.NoError(t, err)

	sdkDir := filepath.Join(tempRoot, "sdk")
	require.NoError(t, WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, fixedClock))

	testFilePath := filepath.Join(filepath.Dir(currentFile), "test_unwrap_envelope.js")
	cmd := exec.Command(bunPath, "test", testFilePath)
	cmd.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(
		t,
		err,
		"TypeScript strict envelope runtime test failed:\n%s",
		string(output),
	)
}

func TestFetchTransportRuntime_TypeScript(t *testing.T) {
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun is required for the TypeScript fetch transport runtime test: %v", err))
	}

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve current test file path")

	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "resolve temp dir")

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	_, apiOutput, parseable := loadFixtureAPI(t)

	sdkOutput, err := Generate(apiOutput, parseable, fixedClock)
	require.NoError(t, err)

	sdkDir := filepath.Join(tempRoot, "sdk")
	require.NoError(t, WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, fixedClock))

	// No bun install: the generated client only imports ./types, so the SDK's
	// peer dependencies are never resolved by this suite.
	testFilePath := filepath.Join(filepath.Dir(currentFile), "test_fetch_transport.js")
	cmd := exec.Command(bunPath, "test", testFilePath)
	cmd.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(
		t,
		err,
		"TypeScript fetch transport runtime test failed:\n%s",
		string(output),
	)
}
