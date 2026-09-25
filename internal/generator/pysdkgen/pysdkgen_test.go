package pysdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

func loadFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-api",
		ModulePath:  "example.com/schemas/api/fixture-api",
		TypesModule: "example.com/schemas/types/go/fixture-api",
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return output
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "", "", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, sdkOutput.PackageName, "sdk.py"))
	if err != nil {
		t.Fatalf("read sdk.py: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty sdk.py")
	}
}

// TestGeneratedSDKRetriesNetworkErrorsForReadRequests: the generated client
// retries a GET, HEAD or OPTIONS request that failed with a NetworkError, up
// to max_network_retries times with exponential backoff, and never retries
// another method, since a write may have reached the server before the
// connection dropped.
func TestGeneratedSDKRetriesNetworkErrorsForReadRequests(t *testing.T) {
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	apiOutput := loadFixtureAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", codegen.DefaultClock())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}

	pkg := sdkOutput.PackageName
	script := fmt.Sprintf(networkRetryProbe, pkg, filepath.Join(outDir, pkg), pkg, pkg, pkg, pkg)
	command := exec.Command(python, "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated SDK network retry probe failed: %v\n%s", err, output)
	}
}

// networkRetryProbe is formatted with the package name, the package
// directory and the package name four more times. It imports client.py
// without the package __init__, so it needs neither the types package nor
// pydantic.
const networkRetryProbe = `
import sys
import types
from unittest.mock import patch

package = types.ModuleType(%q)
package.__path__ = [%q]
sys.modules[package.__name__] = package

from %s.client import ClientConfig, SyncHTTPClient
from %s.errors import NetworkError

client = SyncHTTPClient(ClientConfig(base_url="http://example.test", max_network_retries=2))
for method in ("GET", "HEAD", "OPTIONS"):
    with patch.object(client, "_execute", side_effect=[NetworkError("reset"), NetworkError("reset"), {"ok": True}]) as execute, patch("%s.client.time.sleep") as sleep:
        assert client.request(method, "/health") == {"ok": True}, method
        assert execute.call_count == 3, (method, execute.call_count)
        assert [c.args for c in sleep.call_args_list] == [(1,), (2,)], (method, sleep.call_args_list)

with patch.object(client, "_execute", side_effect=NetworkError("reset")) as execute, patch("%s.client.time.sleep"):
    try:
        client.request("GET", "/health")
    except NetworkError:
        pass
    else:
        raise AssertionError("GET: expected NetworkError once the retries are spent")
    assert execute.call_count == 3, execute.call_count

for method in ("POST", "PUT", "PATCH", "DELETE"):
    with patch.object(client, "_execute", side_effect=NetworkError("reset")) as execute:
        try:
            client.request(method, "/mutate", body={})
        except NetworkError:
            pass
        else:
            raise AssertionError(method + ": expected NetworkError")
        assert execute.call_count == 1, (method, execute.call_count)

default = SyncHTTPClient(ClientConfig(base_url="http://example.test"))
assert default._max_network_retries == 3, default._max_network_retries
`

func TestGenerateDefaultPackageNames(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	apiOutput.SchemaName = "web-admin-api"

	sdkOutput, err := Generate(apiOutput, "", "", codegen.DefaultClock())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if sdkOutput.PackageName != "schemas_web_admin_api_sdk" {
		t.Fatalf("PackageName = %q, want %q", sdkOutput.PackageName, "schemas_web_admin_api_sdk")
	}
	if sdkOutput.TypesPackage != "schemas_types_web_admin_api" {
		t.Fatalf("TypesPackage = %q, want %q", sdkOutput.TypesPackage, "schemas_types_web_admin_api")
	}
}

func TestGenerateConfiguredPackageNames(t *testing.T) {
	apiOutput := loadFixtureAPI(t)

	sdkOutput, err := Generate(apiOutput, "custom-api-sdk", "custom-api-types", codegen.DefaultClock())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if sdkOutput.PackageName != "custom_api_sdk" {
		t.Fatalf("PackageName = %q, want %q", sdkOutput.PackageName, "custom_api_sdk")
	}
	if sdkOutput.TypesPackage != "custom_api_types" {
		t.Fatalf("TypesPackage = %q, want %q", sdkOutput.TypesPackage, "custom_api_types")
	}
}
