package cloudflare_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The fixture's account, its token and the token's id.
const (
	accountID     = "0123456789abcdef0123456789abcdef"
	tokenID       = "f00df00df00df00df00df00df00df00d"
	engineerToken = "the-engineers-token"
)

// fakeCloud is Cloudflare in memory. It records each call that changes
// something, so a test reads what a second run changed.
type fakeCloud struct {
	mu      sync.Mutex
	changes []string

	// userToken makes the token a user token, which the account's
	// endpoint does not verify.
	userToken bool

	subdomain string
	groups    []cloudflare.PermissionGroup
	stores    []cloudflare.SecretsStore
	secrets   map[string]map[string]*fakeSecret // store, then name
	buckets   map[string]bool
	objects   map[string][]byte // bucket/key
	d1        map[string]string // name, then uuid
	nextID    int
}

type fakeSecret struct {
	id    string
	value string
}

var _ cloudflare.Cloud = (*fakeCloud)(nil)

// permissionGroups are the groups the fake account lists, each with a
// fixed id derived from its name, which the golden graph holds.
var permissionGroups = func() []cloudflare.PermissionGroup {
	account := []string{
		"Workers Scripts Write", "Workers Scripts Read", "D1 Write", "D1 Read",
		"Workers R2 Storage Write", "Workers R2 Storage Read", "Secrets Store Write", "Secrets Store Read",
		"Account API Tokens Write", "Account API Tokens Read", "Account Settings Read",
	}
	zone := []string{"Workers Routes Write", "Workers Routes Read", "DNS Write", "DNS Read", "Zone Read"}
	bucket := []string{"Workers R2 Storage Bucket Item Write", "Workers R2 Storage Bucket Item Read"}
	var out []cloudflare.PermissionGroup
	add := func(names []string, scope string) {
		for _, name := range names {
			sum := sha256.Sum256([]byte(name))
			out = append(out, cloudflare.PermissionGroup{ID: hex.EncodeToString(sum[:16]), Name: name, Scopes: []string{scope}})
		}
	}
	add(account, "com.cloudflare.api.account")
	add(zone, "com.cloudflare.api.account.zone")
	add(bucket, "com.cloudflare.edge.r2.bucket")
	return out
}()

// groupIDOf is the fixed id of a permission group.
func groupIDOf(name string) string {
	for _, g := range permissionGroups {
		if g.Name == name {
			return g.ID
		}
	}
	return ""
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{
		subdomain: "acme",
		groups:    permissionGroups,
		secrets:   map[string]map[string]*fakeSecret{},
		buckets:   map[string]bool{},
		objects:   map[string][]byte{},
		d1:        map[string]string{},
	}
}

func (c *fakeCloud) record(format string, args ...any) {
	c.changes = append(c.changes, fmt.Sprintf(format, args...))
}

// Changes returns what the calls so far changed.
func (c *fakeCloud) Changes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.changes)
}

func (c *fakeCloud) checkAccount(account string) error {
	if account != accountID {
		return fmt.Errorf("fake: account %s is not the token's", account)
	}
	return nil
}

func (c *fakeCloud) VerifyToken(_ context.Context, account string) (cloudflare.Token, error) {
	if err := c.checkAccount(account); err != nil {
		return cloudflare.Token{}, err
	}
	return cloudflare.Token{ID: tokenID, Account: !c.userToken}, nil
}

func (c *fakeCloud) WorkersSubdomain(_ context.Context, account string) (string, error) {
	return c.subdomain, c.checkAccount(account)
}

func (c *fakeCloud) PermissionGroups(_ context.Context, account string) ([]cloudflare.PermissionGroup, error) {
	return slices.Clone(c.groups), c.checkAccount(account)
}

func (c *fakeCloud) SecretsStores(_ context.Context, account string) ([]cloudflare.SecretsStore, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.stores), c.checkAccount(account)
}

func (c *fakeCloud) CreateSecretsStore(_ context.Context, account, name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := fmt.Sprintf("%032x", 0x5ec0000+c.nextID)
	c.stores = append(c.stores, cloudflare.SecretsStore{ID: id, Name: name})
	c.record("create Secrets Store %s", name)
	return id, c.checkAccount(account)
}

func (c *fakeCloud) Secrets(_ context.Context, account, store, search string) ([]cloudflare.Secret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []cloudflare.Secret
	for name, s := range c.secrets[store] {
		if strings.Contains(name, search) {
			out = append(out, cloudflare.Secret{ID: s.id, Name: name, Status: "active"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, c.checkAccount(account)
}

func (c *fakeCloud) CreateSecret(_ context.Context, account, store, name string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secrets[store] == nil {
		c.secrets[store] = map[string]*fakeSecret{}
	}
	if _, dup := c.secrets[store][name]; dup {
		return fmt.Errorf("fake: secret %s exists", name)
	}
	c.nextID++
	c.secrets[store][name] = &fakeSecret{id: fmt.Sprintf("secret-%d", c.nextID), value: string(value)}
	c.record("create secret %s", name)
	return c.checkAccount(account)
}

func (c *fakeCloud) UpdateSecret(_ context.Context, account, store, id string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, s := range c.secrets[store] {
		if s.id == id {
			s.value = string(value)
			c.record("update secret %s", name)
			return c.checkAccount(account)
		}
	}
	return fmt.Errorf("fake: no secret %s", id)
}

// secretValue returns a secret's value, as only a Worker would see it.
func (c *fakeCloud) secretValue(store, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.secrets[store][name]; s != nil {
		return s.value
	}
	return ""
}

func (c *fakeCloud) EnsureBucket(_ context.Context, account, bucket string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buckets[bucket] {
		return false, c.checkAccount(account)
	}
	c.buckets[bucket] = true
	c.record("create bucket %s", bucket)
	return true, c.checkAccount(account)
}

func (c *fakeCloud) GetObject(_ context.Context, account, bucket, key string) ([]byte, map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.objects[bucket+"/"+key]
	if !ok {
		return nil, nil, fmt.Errorf("fake: %s/%s: %w", bucket, key, fs.ErrNotExist)
	}
	return slices.Clone(data), nil, c.checkAccount(account)
}

func (c *fakeCloud) PutObject(_ context.Context, account, bucket, key string, data []byte, _ map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.buckets[bucket] {
		return fmt.Errorf("fake: no bucket %s", bucket)
	}
	c.objects[bucket+"/"+key] = slices.Clone(data)
	c.record("put %s/%s", bucket, key)
	return c.checkAccount(account)
}

func (c *fakeCloud) DeleteObject(_ context.Context, account, bucket, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.objects[bucket+"/"+key]; ok {
		delete(c.objects, bucket+"/"+key)
		c.record("delete %s/%s", bucket, key)
	}
	return c.checkAccount(account)
}

func (c *fakeCloud) ListObjects(_ context.Context, account, bucket, prefix string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keys []string
	for k := range c.objects {
		if key, ok := strings.CutPrefix(k, bucket+"/"); ok && strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, c.checkAccount(account)
}

func (c *fakeCloud) D1Database(_ context.Context, account, name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkAccount(account); err != nil {
		return "", err
	}
	uuid, ok := c.d1[name]
	if !ok {
		return "", fmt.Errorf("fake: no D1 database %s: %w", name, fs.ErrNotExist)
	}
	return uuid, nil
}

// fakeGitHub is the repository's Actions secrets in memory; unready makes
// it the gh that is missing.
type fakeGitHub struct {
	unready      error
	environments map[string]bool
	secrets      map[string]string // `<environment>/<name>`, `/<name>` for the repository's
	repositories []string
}

var _ cloudflare.GitHub = (*fakeGitHub)(nil)

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{environments: map[string]bool{}, secrets: map[string]string{}}
}

func (g *fakeGitHub) Ready(context.Context) error { return g.unready }

func (g *fakeGitHub) EnsureEnvironment(_ context.Context, repository, environment string) (bool, error) {
	g.repositories = append(g.repositories, repository)
	if g.environments[environment] {
		return false, nil
	}
	g.environments[environment] = true
	return true, nil
}

func (g *fakeGitHub) SetSecret(_ context.Context, repository, environment, name string, value []byte) error {
	g.repositories = append(g.repositories, repository)
	if environment != "" && !g.environments[environment] {
		return fmt.Errorf("fake: no environment %s", environment)
	}
	g.secrets[environment+"/"+name] = string(value)
	return nil
}

// fakeProvisioner applies a graph by remembering it, and reads a token's
// value as `token-of-<node>`: the stand-in for pulumi's SecretOutput.
type fakeProvisioner struct {
	stacktest.FakeProvisioner

	mu       sync.Mutex
	applied  map[string]bool
	backends []registry.StateBackend
}

var (
	_ registry.Provisioner        = (*fakeProvisioner)(nil)
	_ registry.SecretOutputReader = (*fakeProvisioner)(nil)
)

func (p *fakeProvisioner) Apply(ctx context.Context, req registry.ProvisionRequest, step ir.DeployStep) error {
	if err := p.FakeProvisioner.Apply(ctx, req, step); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.applied == nil {
		p.applied = map[string]bool{}
	}
	for _, id := range step.Resources {
		p.applied[id] = true
	}
	p.backends = append(p.backends, req.Backend)
	return nil
}

func (p *fakeProvisioner) SecretOutput(_ context.Context, req registry.ProvisionRequest, out ir.Output) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.applied[out.Resource] || req.Environment.Resources.Resource(out.Resource) == nil {
		return "", errors.New("fake: not applied")
	}
	return "token-of-" + out.Resource, nil
}

// lastBackend is the backend the last apply ran with.
func (p *fakeProvisioner) lastBackend() registry.StateBackend {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.backends) == 0 {
		return registry.StateBackend{}
	}
	return p.backends[len(p.backends)-1]
}

// pulumiStub registers a provisioner under the name the cloudflare target
// names. The real one is the pulumi extension's; a distribution links
// both.
type pulumiStub struct{ prov registry.Provisioner }

func (pulumiStub) Name() string { return "pulumi-stub" }

func (s pulumiStub) Register(r *registry.Registry) error {
	prov := s.prov
	if prov == nil {
		prov = &stacktest.FakeProvisioner{}
	}
	return r.RegisterProvisioner(registry.ProvisionerSpec{
		Name: cloudflare.Provisioner, Extension: "pulumi-stub", Provisioner: prov,
		Tools: []registry.CLITool{{Name: "pulumi", Version: "3.259.0"}},
	})
}

// shell is a shell's environment, read by Extension.Getenv.
type shell map[string]string

func (s shell) getenv(key string) string { return s[key] }

// fixture is the extension over fakes, assembled.
type fixture struct {
	cloud  *fakeCloud
	gh     *fakeGitHub
	prov   *fakeProvisioner
	shell  shell
	reg    *registry.Registry
	random *strings.Reader
}

// newFixture assembles the extension over a fake Cloudflare, a fake gh and
// a fake provisioner, in a shell with the engineer's token and no
// passphrase.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{cloud: newFakeCloud(), gh: newFakeGitHub(), prov: &fakeProvisioner{}, shell: shell{cloudflare.TokenEnv: engineerToken}}
	// The randomness: a passphrase's 32 bytes, then a check's salt.
	f.random = strings.NewReader(strings.Repeat("\x01", 32) + strings.Repeat("\x02", 32) + strings.Repeat("\x03", 64))
	ext := cloudflare.Extension{Cloud: f.cloud, GitHub: f.gh, Getenv: f.shell.getenv, Rand: f.random}
	reg, err := registry.Assemble(registry.DefaultNaming(), &stacktest.Extension{}, ext, pulumiStub{prov: f.prov})
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

func (f *fixture) target(t *testing.T) registry.TargetSpec {
	t.Helper()
	target, ok := f.reg.Target(cloudflare.Target)
	if !ok {
		t.Fatal("the cloudflare target is not registered")
	}
	return target
}

// cloudflareEnv is an environment of the edge stack on the cloudflare
// target with the values given, as resolution writes one: the target has
// no platform yet, so the environment holds nothing to deploy.
func cloudflareEnv(name string, values map[string]any, params ...string) *ir.ResolvedEnvironment {
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "edge-stack",
		Environment: name,
		Target:      cloudflare.Target,
		Provisioner: cloudflare.Provisioner,
		Values:      values,
		Parameters:  params,
		Deployables: []*ir.ResolvedDeployable{},
		Resources:   &ir.ResourceGraph{},
		DeployOrder: []*ir.DeployStep{},
	}
}

// staging is Staging's values before bootstrap records anything.
func staging() *ir.ResolvedEnvironment {
	return cloudflareEnv("Staging", map[string]any{"accountId": accountID})
}
