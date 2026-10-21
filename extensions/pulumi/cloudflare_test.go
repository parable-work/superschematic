package pulumi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/pulumi"
)

// passwordEnvironment is one random password, whose result the provider
// marks secret, as the cloudflare provider marks an account token's value.
func passwordEnvironment() *ir.ResolvedEnvironment {
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "DemoBootstrap",
		Environment: "Shared",
		Target:      "random",
		Provisioner: "pulumi",
		Deployables: []*ir.ResolvedDeployable{},
		Resources: &ir.ResourceGraph{Resources: []*ir.Resource{{
			ID:         "deployer",
			Type:       "random:index/randomPassword:RandomPassword",
			Properties: map[string]any{"length": float64(24), "special": false},
			Phase:      ir.PhaseInfrastructure,
			Owners:     []string{"bootstrap"},
		}}},
		DeployOrder: []*ir.DeployStep{{Step: ir.StepInfrastructure, Resources: []string{"deployer"}}},
	}
}

// TestSecretOutput: a bootstrap reads a secret output of a node it applied
// from the state, decrypted (D64), which Outputs leaves out; a node or an
// output the stack lacks, and an output path, are refused.
func TestSecretOutput(t *testing.T) {
	requirePulumi(t)
	ctx := context.Background()
	p := pulumi.Extension{
		ProviderVersions: map[string]string{"random": randomVersion},
		Env:              map[string]string{"PULUMI_CONFIG_PASSPHRASE": "superschematic-test"},
	}.Provisioner()
	env := passwordEnvironment()
	req := renderRequest(t, p, env, nil, passphraseBackend(t))
	if _, err := p.SecretOutput(ctx, req, ir.Output{Resource: "deployer", Name: "result"}); err == nil || !strings.Contains(err.Error(), "apply it first") {
		t.Fatalf("before the apply: %v", err)
	}
	if err := p.Apply(ctx, req, *env.DeployOrder[0]); err != nil {
		t.Fatal(err)
	}
	value, err := p.SecretOutput(ctx, req, ir.Output{Resource: "deployer", Name: "result"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{24}$`).MatchString(value) {
		t.Errorf("the password is %q", value)
	}
	if out := outputs(t, p, req); out["deployer"]["result"] != nil {
		t.Error("Outputs holds the secret")
	}
	for _, tc := range []struct {
		out  ir.Output
		want string
	}{
		{ir.Output{Resource: "deployer", Name: "nothing"}, "has no output nothing"},
		{ir.Output{Resource: "planner", Name: "result"}, "has no node planner"},
		{ir.Output{Resource: "deployer", Name: "keepers.a"}, "is a path"},
	} {
		if _, err := p.SecretOutput(ctx, req, tc.out); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.out, err, tc.want)
		}
	}
	if err := p.Destroy(ctx, req); err != nil {
		t.Fatal(err)
	}
}

// s3StandIn records the requests the pulumi CLI makes of an S3 endpoint
// and refuses each, as a bucket the credentials do not reach would.
type s3StandIn struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (s *s3StandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Clone(context.Background()))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
}

// TestCloudflareStateReachesTheCLI: the backend the cloudflare target's
// state store gives a run reaches the pulumi CLI, its URL and its Env: the
// CLI reads the bucket path-style at the URL's endpoint, here a local
// stand-in for R2's, signed in region auto with the token's id as the
// access key (D64). The shell's AWS session token and profile, here a
// stale one and one the machine lacks, do not reach the backend.
func TestCloudflareStateReachesTheCLI(t *testing.T) {
	requirePulumi(t)
	for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION", "PULUMI_CONFIG_PASSPHRASE", "PULUMI_CONFIG_PASSPHRASE_FILE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_SESSION_TOKEN", "a-stale-session-token")
	t.Setenv("AWS_PROFILE", "a-profile-this-machine-lacks")
	standIn := &s3StandIn{}
	srv := httptest.NewServer(standIn)
	t.Cleanup(srv.Close)

	ext := cloudflare.Extension{
		Cloud:  verifiedToken{},
		Getenv: shellEnv{cloudflare.TokenEnv: "the-token", cloudflare.PassphraseEnv: "the-passphrase"}.get,
	}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, pulumi.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Target(cloudflare.Target)
	env := sharedEnvironment()
	env.Values = map[string]any{"accountId": strings.Repeat("0", 32)}
	backend, err := target.State.Backend(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if backend.URL != cloudflare.StateURL(cloudflare.R2Endpoint(strings.Repeat("0", 32))) {
		t.Fatalf("the state is at %s", backend.URL)
	}
	backend.URL = cloudflare.StateURL(srv.URL)

	p := pulumi.Extension{ProviderVersions: map[string]string{"random": randomVersion}}.Provisioner()
	req := renderRequest(t, p, sharedEnvironment(), nil, backend)
	if _, err := p.Plan(context.Background(), req); err == nil {
		t.Fatal("a plan against a refusing stand-in passed")
	}
	standIn.mu.Lock()
	defer standIn.mu.Unlock()
	if len(standIn.requests) == 0 {
		t.Fatal("the CLI made no request of the S3 endpoint")
	}
	r := standIn.requests[0]
	if !strings.HasPrefix(r.URL.Path, "/"+cloudflare.StateBucket+"/") && r.URL.Path != "/"+cloudflare.StateBucket {
		t.Errorf("the CLI asked for %s, not a path-style URL in %s", r.URL, cloudflare.StateBucket)
	}
	auth := r.Header.Get("Authorization")
	if !regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=token-id/\d{8}/auto/s3/aws4_request, `).MatchString(auth) {
		t.Errorf("the CLI signed with %q, not the token's id in region auto", auth)
	}
	if token := r.Header.Get("X-Amz-Security-Token"); token != "" {
		t.Errorf("the CLI sent the shell's session token %q", token)
	}
}

// verifiedToken is a Cloud that verifies any token as `token-id`, all the
// state store calls.
type verifiedToken struct{ cloudflare.Cloud }

func (verifiedToken) VerifyToken(context.Context, string) (cloudflare.Token, error) {
	return cloudflare.Token{ID: "token-id", Account: true}, nil
}

type shellEnv map[string]string

func (s shellEnv) get(key string) string { return s[key] }
