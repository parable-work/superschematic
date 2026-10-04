package rustsdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

const webhooksService = "fixture-webhooks-api"

// loadWebhooksAPI loads fixture-webhooks-api: webhook.receiveStripeEvent,
// webhook.receiveGithubEvent and the manual webhook.receiveRawGithubEvent,
// each @webhook, and event.getEvent, which is not a webhook.
func loadWebhooksAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, webhooksService))
	if err != nil {
		t.Fatalf("load %s: %v", webhooksService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: webhooksService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiOutput
}

func generateWebhooksSDK(t *testing.T, apiOutput *apigen.APIOutput) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(webhooksService), naming.Default().RustTypesCrate(webhooksService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return sdkOutput
}

// TestTheSDKHasNoMethodOrToolForAWebhook: a webhook is called by a third
// party, not by the service's clients, so the Rust SDK leaves it out, as
// the Go and TypeScript SDKs do: no method, no tool, and no validation
// schema for its input. Only event.get_event remains.
func TestTheSDKHasNoMethodOrToolForAWebhook(t *testing.T) {
	apiOutput := loadWebhooksAPI(t)
	sdkOutput := generateWebhooksSDK(t, apiOutput)

	var methods []string
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			methods = append(methods, namespace.Name+"."+endpoint.MethodName)
		}
	}
	if len(methods) != 1 || methods[0] != "event.get_event" {
		t.Errorf("SDK methods = %v, want [event.get_event]", methods)
	}
	if strings.Contains(sdkOutput.InputSchemasJSON, "PaymentEvent") {
		t.Errorf("InputSchemasJSON holds the webhook input PaymentEvent: %s", sdkOutput.InputSchemasJSON)
	}

	toolsOutput, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	var tools, toolNamespaces []string
	for _, tool := range toolsOutput.Tools {
		tools = append(tools, tool.Name)
	}
	for _, namespace := range toolsOutput.Namespaces {
		toolNamespaces = append(toolNamespaces, namespace.Name)
	}
	if len(tools) != 1 || tools[0] != "event.getEvent" {
		t.Errorf("tools = %v, want [event.getEvent]", tools)
	}
	if len(toolNamespaces) != 1 || toolNamespaces[0] != "event" {
		t.Errorf("tool namespaces = %v, want [event]", toolNamespaces)
	}
}

// TestWriteSDKGoldenWebhooks pins every file of the Rust SDK crate for
// fixture-webhooks-api, tool documents included, none of which has a
// webhook. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestWriteSDKGoldenWebhooks -update
func TestWriteSDKGoldenWebhooks(t *testing.T) {
	apiOutput := loadWebhooksAPI(t)
	sdkOutput := generateWebhooksSDK(t, apiOutput)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", webhooksService)
	typesDir := filepath.Join(root, "types", "rust", webhooksService)
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sdkDir, "src", "namespaces", "webhook.rs")); !os.IsNotExist(err) {
		t.Errorf("src/namespaces/webhook.rs is written (stat error %v)", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", webhooksService))
}
