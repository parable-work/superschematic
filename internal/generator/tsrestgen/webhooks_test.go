package tsrestgen

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

const webhooksAPI = "fixture-webhooks-api"

// webhooksFixture is fixture-webhooks-api with apigen's endpoints for it:
// webhook.receiveStripeEvent (@hmacVerified stripe, @rateLimit),
// webhook.receiveGithubEvent (@hmacVerified github, @requirePermission),
// webhook.receiveRawGithubEvent (@hmacVerified github,
// @manualRouteRegistration) and event.getEvent, which is not a webhook.
func webhooksFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, webhooksAPI))
	if err != nil {
		t.Fatalf("load %s: %v", webhooksAPI, err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: webhooksAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: webhooksAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenWebhooks pins the package for fixture-webhooks-api.
// Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenWebhooks -update
func TestWriteAPIGoldenWebhooks(t *testing.T) {
	checkGolden(t, generateFixture(t, webhooksFixture(t)), webhooksAPI)
}

// TestGenerateWebhooksShape: each @hmacVerified operation, the manual one
// included, carries its provider, and Implementations needs a verifier for
// each provider once.
func TestGenerateWebhooksShape(t *testing.T) {
	output := generateFixture(t, webhooksFixture(t))
	if want := []string{"github", "stripe"}; !slices.Equal(output.WebhookProviders, want) {
		t.Errorf("WebhookProviders = %v, want %v", output.WebhookProviders, want)
	}
	byName := endpointsByName(output)
	for name, want := range map[string]string{
		"receiveStripeEvent":    "stripe",
		"receiveGithubEvent":    "github",
		"receiveRawGithubEvent": "github",
		"getEvent":              "",
	} {
		if got := byName[name].WebhookProvider; got != want {
			t.Errorf("%s WebhookProvider = %q, want %q", name, got, want)
		}
	}
}

// TestGeneratedWebhooksRouter type-checks the generated package for
// fixture-webhooks-api and drives it under bun over HTTP: buildRouter
// throws without a verifier for each provider, a provider's verifier runs
// before the rate limit, the body limit and the permission check of its
// routes, the manual one included, the route reads the body the verifier
// read, and a route that is not a webhook runs no verifier.
func TestGeneratedWebhooksRouter(t *testing.T) {
	tree := materializeAPI(t, webhooksFixture(t))
	tree.typeCheck(t)
	tree.runTest(t, "webhooks_runtime.test.ts")
}
