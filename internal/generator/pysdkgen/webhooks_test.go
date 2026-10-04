package pysdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
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

// TestTheSDKHasNoMethodForAWebhook: a webhook is called by a third party,
// not by the service's clients, so the Python SDK leaves it out, as the Go
// and TypeScript SDKs do. Only event.get_event remains, and no webhook
// namespace is written.
func TestTheSDKHasNoMethodForAWebhook(t *testing.T) {
	sdkOutput, err := Generate(loadWebhooksAPI(t), "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var methods []string
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			methods = append(methods, namespace.Name+"."+endpoint.MethodName)
		}
	}
	if len(methods) != 1 || methods[0] != "event.get_event" {
		t.Errorf("SDK methods = %v, want [event.get_event]", methods)
	}

	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	webhookModule := filepath.Join(outDir, sdkOutput.PackageName, "namespaces", "webhook.py")
	if _, err := os.Stat(webhookModule); !os.IsNotExist(err) {
		t.Errorf("namespaces/webhook.py is written (stat error %v)", err)
	}
}

// TestWriteSDKGoldenWebhooks pins every file of the Python SDK for
// fixture-webhooks-api, which has no method for any of its webhooks.
// Regenerate with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenWebhooks -update
func TestWriteSDKGoldenWebhooks(t *testing.T) {
	sdkOutput, err := Generate(loadWebhooksAPI(t), "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", webhooksService))
}
