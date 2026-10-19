package cloudflare_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// TestStateBackend: the state is the account's R2 bucket through Pulumi's
// S3 backend, with the passphrase secrets provider; Env hands the run the
// token's S3 credentials, its id and the SHA-256 of its value, and the
// shell's passphrase. A run without a passphrase or a token is refused
// before anything calls Cloudflare.
func TestStateBackend(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the passphrase"
	state := f.target(t).State
	ctx := context.Background()
	backend, err := state.Backend(ctx, staging())
	if err != nil {
		t.Fatal(err)
	}
	if want := "s3://superschematic-state?endpoint=https%3A%2F%2F" + accountID + ".r2.cloudflarestorage.com&region=auto&use_path_style=true"; backend.URL != want {
		t.Errorf("URL = %s, want %s", backend.URL, want)
	}
	sum := sha256.Sum256([]byte(engineerToken))
	want := map[string]string{
		"AWS_ACCESS_KEY_ID":        tokenID,
		"AWS_SECRET_ACCESS_KEY":    hex.EncodeToString(sum[:]),
		"PULUMI_CONFIG_PASSPHRASE": "the passphrase",
	}
	if backend.SecretsProvider != "passphrase" || !mapsEqual(backend.Env, want) {
		t.Errorf("backend = %+v", backend)
	}

	delete(f.shell, cloudflare.PassphraseEnv)
	if _, err := state.Backend(ctx, staging()); err == nil || !strings.Contains(err.Error(), "set PULUMI_CONFIG_PASSPHRASE to the passphrase of account "+accountID) {
		t.Errorf("no passphrase: %v", err)
	}
	f.shell[cloudflare.PassphraseEnv] = "the passphrase"
	delete(f.shell, cloudflare.TokenEnv)
	if _, err := state.Backend(ctx, staging()); err == nil || !strings.Contains(err.Error(), "set CLOUDFLARE_API_TOKEN") {
		t.Errorf("no token: %v", err)
	}
}

// TestManifests: a run's manifest is written as the current one and as a
// copy under the time, since R2 keeps no versions; a delete removes the
// current one and keeps the history.
func TestManifests(t *testing.T) {
	cloud := newFakeCloud()
	cloud.buckets[cloudflare.StateBucket] = true
	at := time.Date(2026, 10, 19, 13, 28, 0, 123e6, time.FixedZone("x", 3600))
	ext := cloudflare.Extension{Cloud: cloud, Getenv: shell{cloudflare.TokenEnv: engineerToken}.getenv, Now: func() time.Time { return at }}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, pulumiStub{})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Target(cloudflare.Target)
	ctx := context.Background()
	run := registry.Run{Environment: cloudflareEnv("Preview", map[string]any{"accountId": accountID}, "pr"), Parameters: map[string]string{"pr": "12"}}
	if _, err := target.State.ReadManifest(ctx, run); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("ReadManifest before a deploy: %v", err)
	}
	if err := target.State.WriteManifest(ctx, run, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	current := "superschematic/manifests/edge-stack/Preview.pr-12.json"
	history := "superschematic/manifests/edge-stack/Preview.pr-12/2026-10-19T12:28:00.123Z.json"
	if got := cloud.Changes(); !slices.Equal(got, []string{"put superschematic-state/" + history, "put superschematic-state/" + current}) {
		t.Errorf("the write put %q", got)
	}
	if data, err := target.State.ReadManifest(ctx, run); err != nil || string(data) != `{"v":1}` {
		t.Errorf("ReadManifest = %s, %v", data, err)
	}
	if err := target.State.DeleteManifest(ctx, run); err != nil {
		t.Fatal(err)
	}
	keys, _ := cloud.ListObjects(ctx, accountID, cloudflare.StateBucket, cloudflare.ManifestPrefix)
	if !slices.Equal(keys, []string{history}) {
		t.Errorf("after the delete the bucket holds %v, want the history alone", keys)
	}
}

// TestSecretStore: a secret is named after the stack, the environment, the
// declaring type and the field, which a parameterized environment's
// members share; Set creates it or replaces its value; Exists and List
// read names; Get refuses. Before bootstrap records the store, each says
// to run it.
func TestSecretStore(t *testing.T) {
	f := newFixture(t)
	secrets := f.target(t).Secrets
	ctx := context.Background()
	store := strings.Repeat("5", 32)
	f.cloud.stores = []cloudflare.SecretsStore{{ID: store, Name: "default_secrets_store"}}
	env := cloudflareEnv("Preview", map[string]any{"accountId": accountID}, "pr")
	env.Secrets = []*ir.StackSecret{{ID: "PaymentsSecrets.STRIPE_KEY"}, {ID: "PaymentsSecrets.WEBHOOK_KEY"}}

	if err := secrets.Set(ctx, env, "PaymentsSecrets.STRIPE_KEY", []byte("sk_1")); err == nil || !strings.Contains(err.Error(), "run stack bootstrap Preview") {
		t.Errorf("Set before bootstrap: %v", err)
	}
	env.Values["secretsStoreId"] = store

	name := "edge-stack-Preview-PaymentsSecrets-STRIPE_KEY"
	if got := cloudflare.SecretName("edge-stack", "Preview", "PaymentsSecrets.STRIPE_KEY"); got != name {
		t.Errorf("SecretName = %s", got)
	}
	if ok, err := secrets.Exists(ctx, env, "PaymentsSecrets.STRIPE_KEY"); ok || err != nil {
		t.Errorf("Exists before Set = %v, %v", ok, err)
	}
	if err := secrets.Set(ctx, env, "PaymentsSecrets.STRIPE_KEY", []byte("sk_1")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, env, "PaymentsSecrets.STRIPE_KEY", []byte("sk_2")); err != nil {
		t.Fatal(err)
	}
	if got := f.cloud.Changes(); !slices.Equal(got, []string{"create secret " + name, "update secret " + name}) {
		t.Errorf("Set changed %q", got)
	}
	if got := f.cloud.secretValue(store, name); got != "sk_2" {
		t.Errorf("the store holds %q", got)
	}
	if ok, err := secrets.Exists(ctx, env, "PaymentsSecrets.STRIPE_KEY"); !ok || err != nil {
		t.Errorf("Exists after Set = %v, %v", ok, err)
	}
	// Another environment's secret of the same field is its own.
	staging := cloudflareEnv("Staging", map[string]any{"accountId": accountID, "secretsStoreId": store})
	staging.Secrets = env.Secrets
	if ok, _ := secrets.Exists(ctx, staging, "PaymentsSecrets.STRIPE_KEY"); ok {
		t.Error("Staging reads Preview's secret")
	}
	if ids, err := secrets.List(ctx, env); err != nil || !slices.Equal(ids, []string{"PaymentsSecrets.STRIPE_KEY"}) {
		t.Errorf("List = %v, %v", ids, err)
	}
	if _, err := secrets.Get(ctx, env, "PaymentsSecrets.STRIPE_KEY"); err == nil || !strings.Contains(err.Error(), "only to a Worker that binds it") {
		t.Errorf("Get = %v", err)
	}
}
