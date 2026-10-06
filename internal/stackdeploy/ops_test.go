package stackdeploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestManifestRoundTrip writes a manifest, with a failed rollout's
// expanded schema and a pending phase, and reads it back: the resolved
// environment keeps its references, and the bytes are the same.
func TestManifestRoundTrip(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Preview")
	plan, err := (&planner{to: 2}).plan("shop-db", "postgres", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &stackdeploy.Manifest{
		Version:     stackdeploy.ManifestVersion,
		Stack:       env.Stack,
		Environment: env.Environment,
		Run:         "Preview.pr-7",
		Parameters:  map[string]string{"pr": "7"},
		Status:      stackdeploy.StatusFailed,
		Step:        "migrate expand",
		Error:       "lock timeout",
		Time:        fixedNow(),
		Resolved:    env,
		Services:    map[string]string{"shop-api": "sha256:1"},
		Images:      images(1),
		Databases: map[string]map[string]*stackdeploy.AppliedSchema{"shop-db": {"shop-db": {
			Dialect: "postgres",
			Pending: &stackdeploy.PendingMigration{Phase: ir.MigrationExpand, Plan: plan},
		}}},
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := stackdeploy.UnmarshalManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := back.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Errorf("the manifest changed in a round trip:\n%s\n---\n%s", data, again)
	}
	name := back.Resolved.Deployable("shop-api").ResourceName
	if _, ok := name.(ir.Concat); !ok {
		t.Errorf("shop-api's name read back as %T, want the reference to the parameter", name)
	}
	if got := back.Databases["shop-db"]["shop-db"].Pending.Plan; got.Hash != plan.Hash || !bytes.Equal(got.Document, plan.Document) {
		t.Errorf("the pending plan read back as %+v", got)
	}

	var wrong map[string]any
	if err := json.Unmarshal(data, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong["version"] = 2
	data, _ = json.Marshal(wrong)
	if _, err := stackdeploy.UnmarshalManifest(data); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Errorf("a manifest of version 2: %v", err)
	}
}

// TestSetSecrets asks for values through a fake terminal.
func TestSetSecrets(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	ctx := context.Background()
	options := func(only string, term *terminal) stackdeploy.SecretsOptions {
		return stackdeploy.SecretsOptions{Options: f.options(t, env, nil), Only: only, Prompter: term}
	}

	// Every secret and credential without a value.
	term := &terminal{answers: []string{"sk_live_1", "dns-1"}}
	stored, err := stackdeploy.SetSecrets(ctx, options("", term))
	if err != nil {
		t.Fatal(err)
	}
	wantPrompts := []string{
		"Value of PaymentsSecrets.STRIPE_KEY (read by Orders, shop-api): ",
		"An API token for the fake DNS zone, for DNS platform fake.dns (API_TOKEN): ",
	}
	if !slices.Equal(term.prompts, wantPrompts) || !slices.Equal(stored, []string{"PaymentsSecrets.STRIPE_KEY", "fake.dns:API_TOKEN"}) {
		t.Errorf("prompts %q, stored %v", term.prompts, stored)
	}
	if ids, _ := f.ext.Secrets.List(ctx, env); !slices.Equal(ids, stored) {
		t.Errorf("the store lists %v", ids)
	}

	// Nothing left to ask for.
	term = &terminal{}
	if stored, err := stackdeploy.SetSecrets(ctx, options("", term)); err != nil || len(stored) > 0 || len(term.prompts) > 0 {
		t.Errorf("a second run: stored %v, prompts %v, %v", stored, term.prompts, err)
	}

	// A named secret is asked for though it has a value.
	term = &terminal{answers: []string{"sk_live_2"}}
	if _, err := stackdeploy.SetSecrets(ctx, options("PaymentsSecrets.STRIPE_KEY", term)); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ext.Secrets.Get(ctx, env, "PaymentsSecrets.STRIPE_KEY"); string(got) != "sk_live_2" {
		t.Errorf("stored %q", got)
	}
	if log := f.log.String(); strings.Contains(log, "sk_live") || strings.Contains(log, "dns-1") || !strings.Contains(log, "stored a value of PaymentsSecrets.STRIPE_KEY") {
		t.Errorf("log:\n%s", log)
	}

	for _, tc := range []struct {
		name, only string
		answers    []string
		want       string
	}{
		{"unknown", "PaymentsSecrets.OTHER", nil, "has no secret PaymentsSecrets.OTHER (its secrets: PaymentsSecrets.STRIPE_KEY, fake.dns:API_TOKEN)"},
		{"empty value", "PaymentsSecrets.STRIPE_KEY", []string{""}, "no value entered for PaymentsSecrets.STRIPE_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stackdeploy.SetSecrets(ctx, options(tc.only, &terminal{answers: tc.answers}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetSecrets = %v, want an error containing %q", err, tc.want)
			}
		})
	}

	t.Run("not created", func(t *testing.T) {
		f := newFixture(t)
		f.ext.Secrets.Uncreated = []string{"PaymentsSecrets.STRIPE_KEY"}
		_, err := stackdeploy.SetSecrets(ctx, stackdeploy.SecretsOptions{
			Options: f.options(t, env, nil), Only: "PaymentsSecrets.STRIPE_KEY", Prompter: &terminal{answers: []string{"x"}},
		})
		if err == nil || !strings.Contains(err.Error(), "`stack deploy Staging` creates an application secret's storage") {
			t.Fatalf("SetSecrets = %v", err)
		}
	})

	t.Run("a family", func(t *testing.T) {
		// A parameterized environment's secrets are set with no values
		// for its parameters: its members share them.
		preview := f.env(t, "Preview")
		_, err := stackdeploy.SetSecrets(ctx, stackdeploy.SecretsOptions{
			Options: stackdeploy.Options{Registry: f.reg, Run: registry.Run{Environment: preview}}, Only: "PaymentsSecrets.STRIPE_KEY",
			Prompter: &terminal{answers: []string{"x"}},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestBootstrap runs the target's bootstrap with the repository and the
// DNS platform's credentials, and asks for each credential with no value;
// a second run asks for nothing.
func TestBootstrap(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	ctx := context.Background()
	options := func(term *terminal) stackdeploy.BootstrapOptions {
		return stackdeploy.BootstrapOptions{Options: f.options(t, env, nil), Repository: "acme/shop", Prompter: term}
	}
	term := &terminal{answers: []string{"dns-token"}}
	if err := stackdeploy.Bootstrap(ctx, options(term)); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(0); !slices.Equal(got, []string{"bootstrap Staging: repository acme/shop, credentials fake.dns:API_TOKEN"}) {
		t.Errorf("calls %v", got)
	}
	if len(term.prompts) != 1 {
		t.Errorf("prompts %q", term.prompts)
	}
	if got, _ := f.ext.Secrets.Get(ctx, env, registry.CredentialID(stacktest.DNSPlatform, stacktest.DNSToken)); string(got) != "dns-token" {
		t.Errorf("stored %q", got)
	}
	term = &terminal{}
	if err := stackdeploy.Bootstrap(ctx, options(term)); err != nil {
		t.Fatal(err)
	}
	if len(term.prompts) > 0 {
		t.Errorf("a second bootstrap asked %q", term.prompts)
	}

	// Without a terminal, a missing credential fails.
	f = newFixture(t)
	if err := stackdeploy.Bootstrap(ctx, stackdeploy.BootstrapOptions{Options: f.options(t, env, nil)}); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Errorf("bootstrap without a terminal: %v", err)
	}
}

// TestParseImages covers the forms of --image.
func TestParseImages(t *testing.T) {
	got, err := stackdeploy.ParseImages([]string{
		"shop-api=us-east1-docker.pkg.dev/acme/shop/shop-api:v1@" + digest(1),
		"Orders=localhost:5000/orders@" + digest(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"shop-api": "us-east1-docker.pkg.dev/acme/shop/shop-api@" + digest(1),
		"Orders":   "localhost:5000/orders@" + digest(2),
	}
	if !maps(got, want) {
		t.Errorf("ParseImages = %v", got)
	}
	for _, value := range []string{"shop-api", "shop-api=repo:latest", "shop-api=repo@sha256:abc", "=repo@" + digest(1)} {
		if _, err := stackdeploy.ParseImages([]string{value}); err == nil {
			t.Errorf("ParseImages(%q) passed", value)
		}
	}
	if _, err := stackdeploy.ParseImages([]string{"a=r@" + digest(1), "a=r@" + digest(2)}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a repeated server: %v", err)
	}
}
