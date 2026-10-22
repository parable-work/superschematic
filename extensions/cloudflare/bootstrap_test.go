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

// generated and generatedBootstrap are the passphrases the fixture's
// randomness generates, the state's and the bootstrap's: 32 bytes of 0x01
// and of 0x03, in hex.
var (
	generated          = strings.Repeat("01", 32)
	generatedBootstrap = strings.Repeat("03", 32)
)

// givePassphrases puts both passphrases in the fixture's shell.
func (f *fixture) givePassphrases(state, bootstrap string) {
	f.shell[cloudflare.PassphraseEnv] = state
	f.shell[cloudflare.BootstrapPassphraseEnv] = bootstrap
}

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

	// The planner reads, and writes the state bucket's objects alone: it
	// holds no account-wide R2 group, whose read reaches the bootstrap
	// bucket's objects too.
	planner := graph.Resources.Resource("planner")
	policies := planner.Properties["policies"].([]any)
	last := policies[len(policies)-1].(map[string]any)
	if last["resources"] != `{"com.cloudflare.edge.r2.bucket.`+accountID+`_default_superschematic-state":"*"}` {
		t.Errorf("the planner's last policy reaches %v", last["resources"])
	}
	holds := func(node, group string) bool {
		for _, p := range graph.Resources.Resource(node).Properties["policies"].([]any) {
			for _, g := range p.(map[string]any)["permissionGroups"].([]any) {
				if g.(map[string]any)["id"] == groupIDOf(group) {
					return true
				}
			}
		}
		return false
	}
	// Cloudflare's Workflows API, which a job's Workflow node and the job
	// runner call (D64, amended), takes Workers Scripts Write to create,
	// start and terminate, and Workers Scripts Read to read, as its
	// reference lists for each call: the deployer and the preview, which
	// deploy and run jobs, write; the planner, which refreshes the node,
	// reads.
	for node, group := range map[string]string{
		"deployer": cloudflare.WorkflowsWriteGroup,
		"preview":  cloudflare.WorkflowsWriteGroup,
		"planner":  cloudflare.WorkflowsReadGroup,
	} {
		if !holds(node, group) {
			t.Errorf("the %s holds no %s, which the Workflows API asks for", node, group)
		}
	}
	for _, group := range []string{"Workers R2 Storage Read", "Workers R2 Storage Write"} {
		if holds("planner", group) {
			t.Errorf("the planner holds %s", group)
		}
	}
	// The preview deploys a member, and writes no secret's value.
	for group, want := range map[string]bool{
		"Workers Scripts Write": true, "D1 Write": true, "Workers R2 Storage Write": true, "Account API Tokens Write": true,
		"Secrets Store Read": true, "Secrets Store Write": false, "Workers Routes Write": true, "DNS Write": true,
	} {
		if holds("preview", group) != want {
			t.Errorf("the preview holds %s: %v, want %v", group, !want, want)
		}
	}
	if got := graph.DeployOrder[0].Resources; !slices.Equal(got, []string{"deployer", "planner", "preview"}) {
		t.Errorf("the graph applies %v", got)
	}

	groups := slices.DeleteFunc(slices.Clone(permissionGroups), func(g cloudflare.PermissionGroup) bool { return g.Name == "D1 Write" })
	if _, err := cloudflare.BootstrapEnvironment(staging(), groups); err == nil || !strings.Contains(err.Error(), `no permission group named "D1 Write"`) {
		t.Errorf("a missing permission group: %v", err)
	}
}

// TestBootstrap bootstraps a fresh account twice. The first run verifies
// the token, creates the state and the bootstrap buckets, generates each
// one's passphrase, writes its check and prints it once, creates the
// Secrets Store, applies the graph in the bootstrap bucket under the
// bootstrap's own passphrase, and returns the values to record; the
// second, with both passphrases in the shell, changes nothing and returns
// the same.
func TestBootstrap(t *testing.T) {
	f := newFixture(t)
	target := f.target(t)
	ctx := context.Background()
	reveal, log := &bytes.Buffer{}, &bytes.Buffer{}
	result, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, log))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  " + cloudflare.PassphraseEnv + "=" + generated + "\n", "  " + cloudflare.BootstrapPassphraseEnv + "=" + generatedBootstrap + "\n"} {
		if !strings.Contains(reveal.String(), want) {
			t.Errorf("bootstrap printed no %q:\n%s", want, reveal)
		}
	}
	if strings.Contains(log.String(), generated) || strings.Contains(log.String(), generatedBootstrap) {
		t.Errorf("the log holds a passphrase:\n%s", log)
	}
	first := f.cloud.Changes()
	want := []string{
		"create bucket superschematic-state",
		"create bucket superschematic-bootstrap",
		"create superschematic-state/superschematic/passphrase-check",
		"create superschematic-bootstrap/superschematic/passphrase-check",
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
	if calls := f.prov.Calls(); !slices.Equal(calls, []string{"render 3 nodes", "apply infrastructure: deployer, planner, preview"}) {
		t.Errorf("the provisioner ran %q", calls)
	}
	backend := f.prov.lastBackend()
	if backend.URL != cloudflare.BootstrapURL(cloudflare.R2Endpoint(accountID)) || backend.SecretsProvider != "passphrase" ||
		backend.Env["PULUMI_CONFIG_PASSPHRASE"] != generatedBootstrap || backend.Env["AWS_ACCESS_KEY_ID"] != tokenID {
		t.Errorf("the graph applied with backend %+v", backend)
	}

	// The second run, with both passphrases in the shell, finds
	// everything.
	f.givePassphrases(generated, generatedBootstrap)
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

	// A wrong passphrase, and none, of either are refused before anything
	// applies.
	applied := len(f.prov.Calls())
	for _, tc := range []struct{ state, bootstrap, want string }{
		{"another", generatedBootstrap, "PULUMI_CONFIG_PASSPHRASE is not the passphrase of account " + accountID + "'s state"},
		{generated, "another", "SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE is not the passphrase of account " + accountID + "'s bootstrap's state"},
		{"", generatedBootstrap, "account " + accountID + "'s state has a passphrase, which its first bootstrap printed; set PULUMI_CONFIG_PASSPHRASE to it"},
		{generated, "", "account " + accountID + "'s bootstrap's state has a passphrase, which its first bootstrap printed; set SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE to it"},
	} {
		f.givePassphrases(tc.state, tc.bootstrap)
		if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("passphrases %q and %q: %v, want %q", tc.state, tc.bootstrap, err, tc.want)
		}
	}
	if len(f.prov.Calls()) != applied {
		t.Errorf("a refused bootstrap ran the provisioner: %q", f.prov.Calls()[applied:])
	}
}

// TestBootstrapTakesAPassphrase: a first bootstrap with both passphrases in
// the shell takes them, writes each one's check, PBKDF2-SHA256 with its
// salt and its iteration count, and prints nothing; one with neither a
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
	f.givePassphrases("mine", "mine-too")
	reveal := &bytes.Buffer{}
	if _, err := target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil)); err != nil {
		t.Fatal(err)
	}
	if reveal.Len() > 0 {
		t.Errorf("bootstrap printed:\n%s", reveal)
	}
	for _, bucket := range []string{cloudflare.StateBucket, cloudflare.BootstrapBucket} {
		var check struct {
			KDF        string
			Iterations int
			Salt, Key  string
		}
		if err := json.Unmarshal(f.cloud.objects[bucket+"/"+cloudflare.PassphraseCheck], &check); err != nil ||
			check.KDF != "pbkdf2-sha256" || check.Iterations != 600000 || len(check.Salt) != 64 || len(check.Key) != 64 {
			t.Errorf("%s's check is %+v, %v", bucket, check, err)
		}
	}
	if got := f.prov.lastBackend().Env[cloudflare.PassphraseEnv]; got != "mine-too" {
		t.Errorf("the graph applied with passphrase %q, not the bootstrap's", got)
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
			f.givePassphrases("mine", "mine-too")
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
	f.givePassphrases("mine", "mine-too")
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
// token, the deployer's, and the state's passphrase there, and its plan
// job's, the planner's, as repository secrets; a parameterized
// environment's preview job reads the preview's token, not the
// deployer's, from repository secrets alone. Each name holds the stack and
// the environment. The bootstrap's own passphrase is set nowhere, and no
// token reaches the log or the terminal.
func TestBootstrapGitHub(t *testing.T) {
	f := newFixture(t)
	f.givePassphrases("mine", "mine-too")
	target := f.target(t)
	ctx := context.Background()
	reveal, log := &bytes.Buffer{}, &bytes.Buffer{}
	req := f.request(t, staging(), reveal, log)
	req.GitHub = true
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Staging/CLOUDFLARE_DEPLOYER_TOKEN_EDGE_STACK_STAGING": "token-of-deployer",
		"Staging/PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING":  "mine",
		"/CLOUDFLARE_PLANNER_TOKEN_EDGE_STACK_STAGING":         "token-of-planner",
		"/PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING":         "mine",
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
		if strings.Contains(out, "token-of-") || strings.Contains(out, "mine") {
			t.Errorf("a token or a passphrase was printed:\n%s", out)
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
		"/CLOUDFLARE_PREVIEW_TOKEN_EDGE_STACK_PREVIEW": "token-of-preview",
		"/PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_PREVIEW": "mine",
	}
	if !mapsEqual(f.gh.secrets, want) {
		t.Errorf("GitHub holds %v for Preview, want %v", f.gh.secrets, want)
	}
	for _, value := range f.gh.secrets {
		if value == "mine-too" {
			t.Error("GitHub holds the bootstrap's passphrase")
		}
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
	f.givePassphrases("mine", "mine-too")
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
		"The deployer token, edge-stack-deployer:\n\n  token-of-deployer\n\n  gh secret set CLOUDFLARE_DEPLOYER_TOKEN_EDGE_STACK_STAGING --repo acme/edge --env Staging\n",
		"The planner token, edge-stack-planner:\n\n  token-of-planner\n\n  gh secret set CLOUDFLARE_PLANNER_TOKEN_EDGE_STACK_STAGING --repo acme/edge\n",
		"The state's passphrase, the one in PULUMI_CONFIG_PASSPHRASE:\n\n  gh secret set PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING --repo acme/edge\n  gh secret set PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING --repo acme/edge --env Staging\n",
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
	if strings.Contains(out, "mine") || strings.Contains(out, "token-of-preview") {
		t.Errorf("a passphrase or the preview's token was printed:\n%s", out)
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
	f.givePassphrases("mine", "mine-too")
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

// TestBootstrapRefusesALegacyState: an account whose bootstrap state an
// earlier release kept in the state bucket, where the planner's token
// reaches it, is refused before any passphrase or graph, with what to
// revoke and delete.
func TestBootstrapRefusesALegacyState(t *testing.T) {
	f := newFixture(t)
	f.givePassphrases("mine", "mine-too")
	putObjects(t, f.cloud, cloudflare.StateBucket, ".pulumi/stacks/edge-stack-bootstrap/"+accountID+".json")
	_, err := f.target(t).Bootstrap.Bootstrap(context.Background(), f.request(t, staging(), nil, nil))
	for _, want := range []string{
		"keeps stack edge-stack's bootstrap state in superschematic-state (.pulumi/stacks/edge-stack-bootstrap/" + accountID + ".json)",
		"delete edge-stack-deployer, edge-stack-planner and edge-stack-preview in the dashboard",
		"under .pulumi/stacks/edge-stack-bootstrap/",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bootstrap = %v, want it to say %q", err, want)
		}
	}
	if len(f.prov.Calls()) > 0 {
		t.Errorf("the refused bootstrap ran the provisioner: %q", f.prov.Calls())
	}
	if _, ok := f.cloud.objects[cloudflare.StateBucket+"/"+cloudflare.PassphraseCheck]; ok {
		t.Error("the refused bootstrap wrote a check")
	}
}

// racingCloud is the fake whose conditional write of the state's check
// finds one another bootstrap wrote first, for other.
type racingCloud struct {
	*fakeCloud
	t     *testing.T
	other string
}

func (c racingCloud) CreateObject(ctx context.Context, account, bucket, key string, data []byte) (bool, error) {
	if bucket != cloudflare.StateBucket {
		return c.fakeCloud.CreateObject(ctx, account, bucket, key, data)
	}
	theirs, err := cloudflare.NewPassphraseCheck(c.other, []byte("their salt"))
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.fakeCloud.PutObject(ctx, account, bucket, key, theirs, nil); err != nil {
		return false, err
	}
	return c.fakeCloud.CreateObject(ctx, account, bucket, key, data)
}

// TestBootstrapPassphraseRace: two first bootstraps of one account write
// the check at once. The one whose conditional write finds the other's
// check reads it back, goes on when it checks the same passphrase, and
// otherwise refuses, printing no passphrase it generated.
func TestBootstrapPassphraseRace(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, given, other, refused string
	}{
		{"the same passphrase", "mine", "mine", ""},
		{"another passphrase", "mine", "theirs", "another bootstrap of account " + accountID + " wrote the check of its state's passphrase in superschematic-state at the same time, for another passphrase; run stack bootstrap again with PULUMI_CONFIG_PASSPHRASE set"},
		{"a generated one", "", "theirs", "another bootstrap of account " + accountID + " wrote the check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.givePassphrases(tc.given, "mine-too")
			cloud := racingCloud{fakeCloud: f.cloud, t: t, other: tc.other}
			reg, err := registry.Assemble(registry.DefaultNaming(), cloudflare.Extension{Cloud: cloud, GitHub: f.gh, Getenv: f.shell.getenv, Rand: f.random}, pulumiStub{prov: f.prov})
			if err != nil {
				t.Fatal(err)
			}
			target, _ := reg.Target(cloudflare.Target)
			reveal := &bytes.Buffer{}
			_, err = target.Bootstrap.Bootstrap(ctx, f.request(t, staging(), reveal, nil))
			if tc.refused == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("bootstrap = %v, want %q", err, tc.refused)
			}
			if strings.Contains(reveal.String(), generated) {
				t.Errorf("the losing bootstrap printed its passphrase:\n%s", reveal)
			}
		})
	}
}

// TestBootstrapRefusesAnOldCheck: a check of the earlier form, an HMAC, is
// no check this release reads, and bootstrap says how to write it anew.
func TestBootstrapRefusesAnOldCheck(t *testing.T) {
	f := newFixture(t)
	f.givePassphrases("mine", "mine-too")
	putObjects(t, f.cloud, cloudflare.StateBucket)
	f.cloud.objects[cloudflare.StateBucket+"/"+cloudflare.PassphraseCheck] = []byte(`{"salt":"0202","hmac":"0303"}`)
	_, err := f.target(t).Bootstrap.Bootstrap(context.Background(), f.request(t, staging(), nil, nil))
	if err == nil || !strings.Contains(err.Error(), "superschematic/passphrase-check in superschematic-state is no passphrase check this release reads") ||
		!strings.Contains(err.Error(), "delete it and run stack bootstrap again with PULUMI_CONFIG_PASSPHRASE set") {
		t.Errorf("an old check: %v", err)
	}
}

// TestBootstrapKeepsAStatesPassphrase: a bucket that holds Pulumi state
// and no check gets no generated passphrase, which would not decrypt it.
func TestBootstrapKeepsAStatesPassphrase(t *testing.T) {
	f := newFixture(t)
	putObjects(t, f.cloud, cloudflare.StateBucket, ".pulumi/stacks/edge-stack/staging.json")
	_, err := f.target(t).Bootstrap.Bootstrap(context.Background(), f.request(t, staging(), &bytes.Buffer{}, nil))
	if err == nil || !strings.Contains(err.Error(), "superschematic-state holds Pulumi state and no superschematic/passphrase-check, so bootstrap would generate a passphrase that does not decrypt it") {
		t.Errorf("state without a check: %v", err)
	}
}
