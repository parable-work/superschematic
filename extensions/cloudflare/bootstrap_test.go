package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// generated is the passphrase the fixture's randomness generates: 32 bytes
// of 0x01, in hex.
var generated = strings.Repeat("01", 32)

// request is a bootstrap of env with the fixture's provisioner.
func (f *fixture) request(t *testing.T, env *ir.ResolvedEnvironment, reveal *bytes.Buffer, log *bytes.Buffer) registry.BootstrapRequest {
	t.Helper()
	req := registry.BootstrapRequest{Environment: env, Repository: "acme/edge", Provisioner: f.prov, Dir: t.TempDir()}
	if reveal != nil {
		req.Reveal = reveal
	}
	if log != nil {
		req.Log = log
	}
	return req
}

// TestBootstrapGraph checks the bootstrap graph against the golden file,
// with the permission groups' fixed ids, validates every node against the
// pinned schema of its type, and refuses a permission group the account
// does not list.
func TestBootstrapGraph(t *testing.T) {
	f := newFixture(t)
	graph, err := cloudflare.BootstrapEnvironment(staging(), permissionGroups)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range graph.Resources.Resources {
		ok, err := f.reg.ValidateResource(res)
		if !ok || err != nil {
			t.Errorf("node %s (%s): pinned %v, %v", res.ID, res.Type, ok, err)
		}
	}
	if graph.Stack != "edge-stackBootstrap" || graph.Environment != accountID {
		t.Errorf("the graph is stack %s, environment %s; want a Pulumi project edge-stack-bootstrap with a stack per account", graph.Stack, graph.Environment)
	}
	got, err := stack.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(goldenRoot, "bootstrap", "Staging.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the bootstrap graph differs from %s; run go test -update and review the diff", path)
	}

	// The planner reads, and writes the state bucket's objects alone.
	planner := graph.Resources.Resource("planner")
	policies := planner.Properties["policies"].([]any)
	last := policies[len(policies)-1].(map[string]any)
	if last["resources"] != `{"com.cloudflare.edge.r2.bucket.`+accountID+`_default_superschematic-state":"*"}` {
		t.Errorf("the planner's last policy reaches %v", last["resources"])
	}

	groups := slices.DeleteFunc(slices.Clone(permissionGroups), func(g cloudflare.PermissionGroup) bool { return g.Name == "D1 Write" })
	if _, err := cloudflare.BootstrapEnvironment(staging(), groups); err == nil || !strings.Contains(err.Error(), `no permission group named "D1 Write"`) {
		t.Errorf("a missing permission group: %v", err)
	}
}

// TestBootstrap bootstraps a fresh account twice. The first run verifies
// the token, creates the state bucket, generates the passphrase, writes its
// check and prints it once, creates the Secrets Store, applies the graph
// with the state's backend, and returns the values to record; the second,
// with the passphrase in the shell, changes nothing and returns the same.
func TestBootstrap(t *testing.T) {
	f := newFixture(t)
	target := f.target(t)
	ctx := context.Background()
	reveal, log := &bytes.Buffer{}, &bytes.Buffer{}
	result, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, log))
	if err != nil {
		t.Fatal(err)
	}
	if want := "  " + cloudflare.PassphraseEnv + "=" + generated + "\n"; !strings.Contains(reveal.String(), want) {
		t.Errorf("bootstrap printed no passphrase:\n%s", reveal)
	}
	if strings.Contains(log.String(), generated) {
		t.Errorf("the log holds the passphrase:\n%s", log)
	}
	first := f.cloud.Changes()
	want := []string{
		"create bucket superschematic-state",
		"put superschematic-state/superschematic/passphrase-check",
		"create Secrets Store default_secrets_store",
	}
	if !slices.Equal(first, want) {
		t.Errorf("the first bootstrap changed %q, want %q", first, want)
	}
	store := f.cloud.stores[0].ID
	values := []registry.BootstrapValue{
		{Key: "secretsStoreId", Value: store, Beside: "accountId"},
		{Key: "workersSubdomain", Value: "acme", Beside: "accountId"},
	}
	if result == nil || !slices.Equal(result.Values, values) {
		t.Errorf("bootstrap returned %+v, want %+v", result, values)
	}
	if calls := f.prov.Calls(); !slices.Equal(calls, []string{"render 2 nodes", "apply infrastructure: deployer, planner"}) {
		t.Errorf("the provisioner ran %q", calls)
	}
	backend := f.prov.lastBackend()
	if backend.URL != cloudflare.StateURL(cloudflare.R2Endpoint(accountID)) || backend.SecretsProvider != "passphrase" ||
		backend.Env["PULUMI_CONFIG_PASSPHRASE"] != generated || backend.Env["AWS_ACCESS_KEY_ID"] != tokenID {
		t.Errorf("the graph applied with backend %+v", backend)
	}

	// The second run, with the passphrase in the shell, finds everything.
	f.shell[cloudflare.PassphraseEnv] = generated
	reveal.Reset()
	again, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil))
	if err != nil {
		t.Fatal(err)
	}
	if changed := f.cloud.Changes(); len(changed) != len(first) {
		t.Errorf("the second bootstrap changed %q", changed[len(first):])
	}
	if reveal.Len() > 0 {
		t.Errorf("the second bootstrap printed:\n%s", reveal)
	}
	if !slices.Equal(again.Values, values) {
		t.Errorf("the second bootstrap returned %+v", again.Values)
	}

	// A wrong passphrase, and none, are refused before anything applies.
	applied := len(f.prov.Calls())
	f.shell[cloudflare.PassphraseEnv] = "another"
	if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil)); err == nil || !strings.Contains(err.Error(), "is not the passphrase of account") {
		t.Errorf("a wrong passphrase: %v", err)
	}
	delete(f.shell, cloudflare.PassphraseEnv)
	if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil)); err == nil || !strings.Contains(err.Error(), "set PULUMI_CONFIG_PASSPHRASE to it") {
		t.Errorf("no passphrase on a second run: %v", err)
	}
	if len(f.prov.Calls()) != applied {
		t.Errorf("a refused bootstrap ran the provisioner: %q", f.prov.Calls()[applied:])
	}
}

// TestBootstrapTakesAPassphrase: a first bootstrap with a passphrase in the
// shell takes it, writes its check, and prints nothing; one with neither a
// passphrase nor anywhere to print one is refused before it makes one.
func TestBootstrapTakesAPassphrase(t *testing.T) {
	f := newFixture(t)
	target := f.target(t)
	ctx := context.Background()
	if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), nil, nil)); err == nil || !strings.Contains(err.Error(), "nowhere to print it") {
		t.Fatalf("a generated passphrase with nowhere to print it: %v", err)
	}
	if _, ok := f.cloud.objects["superschematic-state/"+cloudflare.PassphraseCheck]; ok {
		t.Error("the refused bootstrap wrote a check")
	}
	f.shell[cloudflare.PassphraseEnv] = "mine"
	reveal := &bytes.Buffer{}
	if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil)); err != nil {
		t.Fatal(err)
	}
	if reveal.Len() > 0 {
		t.Errorf("bootstrap printed:\n%s", reveal)
	}
	var check struct{ Salt, HMAC string }
	if err := json.Unmarshal(f.cloud.objects["superschematic-state/"+cloudflare.PassphraseCheck], &check); err != nil || len(check.Salt) != 64 || len(check.HMAC) != 64 {
		t.Errorf("the check is %+v, %v", check, err)
	}
	if got := f.prov.lastBackend().Env[cloudflare.PassphraseEnv]; got != "mine" {
		t.Errorf("the graph applied with passphrase %q", got)
	}
}

// TestBootstrapSecretsStore: bootstrap keeps the store the values name, or
// the account's one store, and refuses a store the account lacks and an
// account with several and none named.
func TestBootstrapSecretsStore(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		stores  []cloudflare.SecretsStore
		values  map[string]any
		want    string
		refused string
	}{
		{"the one store", []cloudflare.SecretsStore{{ID: strings.Repeat("a", 32), Name: "mine"}}, nil, strings.Repeat("a", 32), ""},
		{"the default among several", []cloudflare.SecretsStore{{ID: strings.Repeat("a", 32), Name: "mine"}, {ID: strings.Repeat("b", 32), Name: "default_secrets_store"}}, nil, strings.Repeat("b", 32), ""},
		{"the recorded one", []cloudflare.SecretsStore{{ID: strings.Repeat("a", 32), Name: "mine"}, {ID: strings.Repeat("b", 32), Name: "default_secrets_store"}}, map[string]any{"secretsStoreId": strings.Repeat("a", 32)}, strings.Repeat("a", 32), ""},
		{"a recorded one the account lacks", nil, map[string]any{"secretsStoreId": strings.Repeat("c", 32)}, "", "is no Secrets Store of account"},
		{"several", []cloudflare.SecretsStore{{ID: strings.Repeat("a", 32), Name: "one"}, {ID: strings.Repeat("b", 32), Name: "two"}}, nil, "", "has several Secrets Stores"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.shell[cloudflare.PassphraseEnv] = "mine"
			f.cloud.stores = tc.stores
			env := staging()
			for k, v := range tc.values {
				env.Values[k] = v
			}
			result, err := f.target(t).Bootstrap.Bootstrap(ctx, f.request(t, env, nil, nil))
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("got %v, want %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Values[0] != (registry.BootstrapValue{Key: "secretsStoreId", Value: tc.want, Beside: "accountId"}) {
				t.Errorf("recorded %+v, want store %s", result.Values[0], tc.want)
			}
			if slices.ContainsFunc(f.cloud.Changes(), func(c string) bool { return strings.HasPrefix(c, "create Secrets Store") }) {
				t.Error("bootstrap created a store beside the account's")
			}
		})
	}
}

// TestBootstrapWithoutASubdomain: an account with no workers.dev subdomain
// is told to choose one, and only secretsStoreId is recorded.
func TestBootstrapWithoutASubdomain(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "mine"
	f.cloud.subdomain = ""
	log := &bytes.Buffer{}
	result, err := f.target(t).Bootstrap.Bootstrap(context.Background(), f.request(t, staging(), nil, log))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Values) != 1 || result.Values[0].Key != "secretsStoreId" {
		t.Errorf("recorded %+v", result.Values)
	}
	if !strings.Contains(log.String(), "has no workers.dev subdomain: choose one in the dashboard") {
		t.Errorf("the log does not say to choose a subdomain:\n%s", log)
	}
}

// TestBootstrapGitHub: with --github and gh ready, bootstrap creates the
// GitHub environment of a plain environment and sets its deploy job's
// token and passphrase there, and its plan job's as repository secrets; a
// parameterized environment's preview job reads repository secrets alone.
// No token reaches the log or the terminal.
func TestBootstrapGitHub(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "mine"
	target := f.target(t)
	ctx := context.Background()
	reveal, log := &bytes.Buffer{}, &bytes.Buffer{}
	req := f.request(t, staging(), reveal, log)
	req.GitHub = true
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Staging/CLOUDFLARE_API_TOKEN":      "token-of-deployer",
		"Staging/PULUMI_CONFIG_PASSPHRASE":  "mine",
		"/CLOUDFLARE_PLANNER_TOKEN_STAGING": "token-of-planner",
		"/PULUMI_CONFIG_PASSPHRASE_STAGING": "mine",
	}
	if !mapsEqual(f.gh.secrets, want) {
		t.Errorf("GitHub holds %v, want %v", f.gh.secrets, want)
	}
	if !f.gh.environments["Staging"] {
		t.Error("no GitHub environment Staging")
	}
	for _, repo := range f.gh.repositories {
		if repo != "acme/edge" {
			t.Errorf("gh called on repository %s", repo)
		}
	}
	for _, out := range []string{reveal.String(), log.String()} {
		if strings.Contains(out, "token-of-") || strings.Contains(out, "=mine") {
			t.Errorf("a token or the passphrase was printed:\n%s", out)
		}
	}

	preview := cloudflareEnv("Preview", map[string]any{"accountId": accountID}, "pr")
	f.gh.secrets = map[string]string{}
	req = f.request(t, preview, reveal, nil)
	req.GitHub = true
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	want = map[string]string{
		"/CLOUDFLARE_PREVIEW_TOKEN_PREVIEW": "token-of-deployer",
		"/PULUMI_CONFIG_PASSPHRASE_PREVIEW": "mine",
	}
	if !mapsEqual(f.gh.secrets, want) {
		t.Errorf("GitHub holds %v for Preview, want %v", f.gh.secrets, want)
	}

	req.Repository = ""
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err == nil || !strings.Contains(err.Error(), "pass --repository") {
		t.Errorf("--github with no repository: %v", err)
	}
}

// TestBootstrapGitHubWithoutGh: without gh, --github prints the commands
// that set each secret, and each token once, and sets nothing.
func TestBootstrapGitHubWithoutGh(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "mine"
	f.gh.unready = errors.New("gh, the GitHub CLI, is not on PATH")
	reveal := &bytes.Buffer{}
	req := f.request(t, staging(), reveal, nil)
	req.GitHub = true
	if _, err := f.target(t).Bootstrap.Bootstrap(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	out := reveal.String()
	for _, want := range []string{
		"gh, the GitHub CLI, is not on PATH.\nSet the generated workflow's secrets yourself: each command below reads the\nvalue from its standard input. Each token is shown this once.\n",
		"The deployer token, edge-stack-deployer:\n\n  token-of-deployer\n\n  gh secret set CLOUDFLARE_API_TOKEN --repo acme/edge --env Staging\n",
		"The planner token, edge-stack-planner:\n\n  token-of-planner\n\n  gh secret set CLOUDFLARE_PLANNER_TOKEN_STAGING --repo acme/edge\n",
		"The state's passphrase, the one in PULUMI_CONFIG_PASSPHRASE:\n\n  gh secret set PULUMI_CONFIG_PASSPHRASE_STAGING --repo acme/edge\n  gh secret set PULUMI_CONFIG_PASSPHRASE --repo acme/edge --env Staging\n",
		"GitHub environment Staging must exist first: gh api --method PUT repos/acme/edge/environments/Staging",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bootstrap printed no %q:\n%s", want, out)
		}
	}
	for _, token := range []string{"token-of-deployer", "token-of-planner"} {
		if n := strings.Count(out, token); n != 1 {
			t.Errorf("%s printed %d times", token, n)
		}
	}
	if strings.Contains(out, "mine") {
		t.Errorf("the passphrase was printed:\n%s", out)
	}
	if len(f.gh.secrets) > 0 {
		t.Errorf("gh set %v", f.gh.secrets)
	}
}

// TestBootstrapRecordsValues runs the core's bootstrap over the target:
// with no schema source, it says what to add beside accountId, the
// Secrets Store's id and the workers.dev subdomain.
func TestBootstrapRecordsValues(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "mine"
	recorded, err := stack.Bootstrap(context.Background(), stack.BootstrapOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: staging()}, Dir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range recorded {
		keys = append(keys, r.Key)
		if r.Outcome != stack.ValueByHand || !strings.Contains(r.Hand, "beside accountId in the cloudflare values of Staging") {
			t.Errorf("%s: %s, %q", r.Key, r.Outcome, r.Hand)
		}
	}
	if !slices.Equal(keys, []string{"secretsStoreId", "workersSubdomain"}) {
		t.Errorf("recorded %v", keys)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
