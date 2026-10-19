package cloudflare

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Bootstrap (docs/stack-model.md, section 18.3) runs once per account with
// the engineer's token, in TokenEnv, and is safe to run again:
//
//  1. it verifies the token, an account token of the environment's
//     account or a user token;
//  2. it creates StateBucket directly, since Pulumi needs it first;
//  3. it generates the state's passphrase, 32 random bytes it prints once,
//     or checks the one in PassphraseEnv against PassphraseCheck, a salt
//     and an HMAC of it under the passphrase, which its first run writes;
//  4. it finds the account's Secrets Store, or creates it;
//  5. it applies the bootstrap graph in Pulumi project `<stack>-bootstrap`,
//     a stack per account: the `<stack>-deployer` and `<stack>-planner`
//     account tokens, with the permission groups' ids read from the API
//     first;
//  6. with --github, it sets each token and the passphrase as the
//     repository's Actions secrets through gh, at the scopes the CI seam
//     names; without gh, it prints the commands and each token once;
//  7. it returns secretsStoreId and workersSubdomain, which the core
//     records beside accountId (D47).

// TypeAccountToken is the type of the bootstrap graph's nodes: an API
// token the account owns, which outlives the person who made it. Its
// `value` output is the token, a secret of the state.
const TypeAccountToken = "cloudflare:index/accountToken:AccountToken"

// secretsStoreName is the name of the Secrets Store bootstrap creates,
// the one the dashboard creates for an account's first secret.
const secretsStoreName = "default_secrets_store"

// passphraseBytes is how many random bytes a generated passphrase holds.
const passphraseBytes = 32

// The resources a token's policy reaches (tokenPolicy.reach).
const (
	reachAccount = "account"
	reachZones   = "zones"
	reachState   = "state"
)

// tokenPolicy is one policy of a bootstrap token: the permission groups,
// by name, it allows on what it reaches.
type tokenPolicy struct {
	reach  string
	groups []string
}

// The scopes of the permission groups each reach takes.
var reachScopes = map[string]string{
	reachAccount: "com.cloudflare.api.account",
	reachZones:   "com.cloudflare.api.account.zone",
	reachState:   "com.cloudflare.edge.r2.bucket",
}

// The two tokens' policies (section 18.3). The deployer edits the account's
// Workers scripts, D1, R2, Secrets Store and account tokens, since a bucket
// edge mints a token (section 18.7), and the Workers routes and DNS of the
// account's zones; so it can grant itself more, as gcp's deployer can. The
// planner reads all of that, and writes the objects of the state bucket
// alone, since a preview takes the stack's lock. Both reach every zone of
// the account: the graph is one per account, and the environments of one
// account may hold their domains in different zones.
var (
	deployerPolicies = []tokenPolicy{
		{reachAccount, []string{"Workers Scripts Write", "D1 Write", "Workers R2 Storage Write", "Secrets Store Write", "Account API Tokens Write"}},
		{reachZones, []string{"Workers Routes Write", "DNS Write"}},
	}
	plannerPolicies = []tokenPolicy{
		{reachAccount, []string{"Workers Scripts Read", "D1 Read", "Workers R2 Storage Read", "Secrets Store Read", "Account API Tokens Read"}},
		{reachZones, []string{"Workers Routes Read", "DNS Read"}},
		{reachState, []string{"Workers R2 Storage Bucket Item Write"}},
	}
)

// The bootstrap graph's nodes: the CI's two tokens.
const (
	deployerNode = "deployer"
	plannerNode  = "planner"
)

// tokenName is the name of one of the stack's CI tokens:
// `<stack>-deployer`, `<stack>-planner`.
func tokenName(stack, role string) string { return kebab(stack) + "-" + role }

// reachResources is a token policy's resources, the JSON object the
// provider takes as a string: the account; every zone of the account; or
// the state bucket's objects.
func reachResources(reach, account string) (string, error) {
	var resources any
	switch reach {
	case reachAccount:
		resources = map[string]any{"com.cloudflare.api.account." + account: "*"}
	case reachZones:
		resources = map[string]any{"com.cloudflare.api.account." + account: map[string]any{"com.cloudflare.api.account.zone.*": "*"}}
	case reachState:
		resources = map[string]any{"com.cloudflare.edge.r2.bucket." + account + "_default_" + StateBucket: "*"}
	default:
		return "", fmt.Errorf("cloudflare: no resources for %s", reach)
	}
	data, err := json.Marshal(resources)
	return string(data), err
}

// groupID finds a permission group by its name, preferring one of scope
// when the name has several.
func groupID(groups []PermissionGroup, name, scope string) (string, error) {
	var found []PermissionGroup
	for _, g := range groups {
		if g.Name == name {
			found = append(found, g)
		}
	}
	for _, g := range found {
		if slices.Contains(g.Scopes, scope) {
			return g.ID, nil
		}
	}
	if len(found) == 1 {
		return found[0].ID, nil
	}
	if len(found) > 1 {
		return "", fmt.Errorf("cloudflare: the account has %d permission groups named %q, and none of scope %s", len(found), name, scope)
	}
	return "", fmt.Errorf("cloudflare: the account has no permission group named %q, which a CI token holds; Cloudflare may have renamed it", name)
}

// BootstrapEnvironment returns the bootstrap graph of env's account as an
// environment the provisioner applies in one step: stack `<Stack>Bootstrap`,
// Pulumi project `<stack>-bootstrap`, environment the account's id, one
// Pulumi stack per account. groups are the permission groups the API
// lists, whose ids the tokens' policies name.
func BootstrapEnvironment(env *ir.ResolvedEnvironment, groups []PermissionGroup) (*ir.ResolvedEnvironment, error) {
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	var nodes []*ir.Resource
	for _, role := range []struct {
		id       string
		policies []tokenPolicy
	}{{deployerNode, deployerPolicies}, {plannerNode, plannerPolicies}} {
		var policies []any
		for _, p := range role.policies {
			resources, err := reachResources(p.reach, v.accountID)
			if err != nil {
				return nil, err
			}
			var ids []any
			for _, name := range p.groups {
				id, err := groupID(groups, name, reachScopes[p.reach])
				if err != nil {
					return nil, err
				}
				ids = append(ids, map[string]any{"id": id})
			}
			policies = append(policies, map[string]any{"effect": "allow", "permissionGroups": ids, "resources": resources})
		}
		nodes = append(nodes, &ir.Resource{
			ID:     role.id,
			Type:   TypeAccountToken,
			Phase:  ir.PhaseInfrastructure,
			Owners: []string{"bootstrap"},
			Properties: map[string]any{
				"accountId": v.accountID,
				"name":      tokenName(env.Stack, role.id),
				"policies":  policies,
			},
		})
	}
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       env.Stack + "Bootstrap",
		Environment: v.accountID,
		Target:      Target,
		Provisioner: Provisioner,
		Values:      map[string]any{"accountId": v.accountID},
		Deployables: []*ir.ResolvedDeployable{},
		Resources:   &ir.ResourceGraph{Resources: nodes},
		DeployOrder: []*ir.DeployStep{{Step: ir.StepInfrastructure, Resources: []string{deployerNode, plannerNode}}},
	}, nil
}

// bootstrapper is the cloudflare target's Bootstrapper.
type bootstrapper struct{ ext Extension }

var _ registry.Bootstrapper = bootstrapper{}

// Bootstrap runs the steps above for req's environment.
func (b bootstrapper) Bootstrap(ctx context.Context, req registry.BootstrapRequest) (*registry.BootstrapResult, error) {
	env := req.Environment
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	account := v.accountID
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	token := b.ext.getenv(TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("cloudflare: set %s to your API token, one that reads account %s, creates R2 buckets and account API tokens, and creates or reads its Secrets Store", TokenEnv, account)
	}
	if req.GitHub && req.Repository == "" {
		return nil, errors.New("cloudflare: --github sets the secrets of the GitHub repository the CI runs in, and the git remote names none; pass --repository owner/name")
	}
	if req.Provisioner == nil || req.Dir == "" {
		return nil, errors.New("cloudflare: bootstrap needs the provisioner and a directory for its program")
	}
	cloud, err := b.ext.cloud()
	if err != nil {
		return nil, err
	}

	// 1. The token and the account.
	tok, err := cloud.VerifyToken(ctx, account)
	if err != nil {
		return nil, err
	}
	kind := "a user token"
	if tok.Account {
		kind = "an account token of " + account
	}
	logf("token %s: active, %s", tok.ID, kind)

	// 2. The state bucket.
	created, err := cloud.EnsureBucket(ctx, account, StateBucket)
	if err != nil {
		return nil, err
	}
	logf("state bucket %s: %s", StateBucket, createdOrKept(created))

	// 3. The passphrase.
	passphrase, err := b.passphrase(ctx, cloud, account, logf, req.Reveal)
	if err != nil {
		return nil, err
	}

	// 4. The Secrets Store.
	store, err := b.secretsStore(ctx, cloud, v, logf)
	if err != nil {
		return nil, err
	}

	// 5. The bootstrap graph.
	groups, err := cloud.PermissionGroups(ctx, account)
	if err != nil {
		return nil, err
	}
	graph, err := BootstrapEnvironment(env, groups)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(req.Dir, "bootstrap")
	if err := req.Provisioner.Render(graph, dir); err != nil {
		return nil, err
	}
	run := registry.ProvisionRequest{Environment: graph, Dir: dir, Backend: stateBackend(account, tok.ID, token, passphrase)}
	logf("apply the bootstrap graph: tokens %s and %s", tokenName(env.Stack, deployerNode), tokenName(env.Stack, plannerNode))
	if err := req.Provisioner.Apply(ctx, run, *graph.DeployOrder[0]); err != nil {
		return nil, err
	}
	logf("bootstrap graph applied")

	// 6. The CI's secrets.
	if req.GitHub {
		if err := b.storeCI(ctx, req, run, passphrase, logf); err != nil {
			return nil, err
		}
	} else {
		logf("the CI's tokens are in the bootstrap's state; stack bootstrap %s --github sets them, and the passphrase, as the repository's Actions secrets", env.Environment)
	}

	// 7. The values to record.
	subdomain, err := cloud.WorkersSubdomain(ctx, account)
	if err != nil {
		return nil, err
	}
	values := []registry.BootstrapValue{{Key: "secretsStoreId", Value: store, Beside: "accountId"}}
	if subdomain == "" {
		logf("account %s has no workers.dev subdomain: choose one in the dashboard (Workers & Pages), where an exposed Worker without a domain is reached, and run bootstrap again", account)
	} else {
		logf("workers.dev subdomain: %s", subdomain)
		values = append(values, registry.BootstrapValue{Key: "workersSubdomain", Value: subdomain, Beside: "accountId"})
	}
	return &registry.BootstrapResult{Values: values}, nil
}

// passphraseCheck is the content of PassphraseCheck: a random salt and its
// HMAC-SHA256 under the passphrase, each in hex.
type passphraseCheck struct {
	Salt string `json:"salt"`
	HMAC string `json:"hmac"`
}

func checkMAC(passphrase string, salt []byte) []byte {
	mac := hmac.New(sha256.New, []byte(passphrase))
	mac.Write(salt)
	return mac.Sum(nil)
}

// passphrase returns the state's passphrase. With a check in the bucket it
// checks the shell's passphrase against it, and refuses a missing or wrong
// one. Without, this is the account's first bootstrap: it takes the shell's
// passphrase, or generates one, writes its check, and only then prints a
// generated one, once, to reveal.
func (b bootstrapper) passphrase(ctx context.Context, cloud Cloud, account string, logf func(string, ...any), reveal io.Writer) (string, error) {
	given := b.ext.getenv(PassphraseEnv)
	data, _, err := cloud.GetObject(ctx, account, StateBucket, PassphraseCheck)
	switch {
	case err == nil:
		var check passphraseCheck
		if err := json.Unmarshal(data, &check); err != nil {
			return "", fmt.Errorf("cloudflare: %s in %s is not a passphrase check: %w", PassphraseCheck, StateBucket, err)
		}
		salt, saltErr := hex.DecodeString(check.Salt)
		want, macErr := hex.DecodeString(check.HMAC)
		if saltErr != nil || macErr != nil || len(salt) == 0 {
			return "", fmt.Errorf("cloudflare: %s in %s is not a passphrase check", PassphraseCheck, StateBucket)
		}
		if given == "" {
			return "", fmt.Errorf("cloudflare: account %s's state has a passphrase, which its first bootstrap printed; set %s to it", account, PassphraseEnv)
		}
		if !hmac.Equal(checkMAC(given, salt), want) {
			return "", fmt.Errorf("cloudflare: %s is not the passphrase of account %s's state, which its first bootstrap printed", PassphraseEnv, account)
		}
		logf("passphrase: %s matches the state's check", PassphraseEnv)
		return given, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	passphrase := given
	if passphrase == "" {
		if reveal == nil {
			return "", fmt.Errorf("cloudflare: bootstrap generates the state's passphrase and prints it once, and has nowhere to print it; set %s to a passphrase of your own", PassphraseEnv)
		}
		raw := make([]byte, passphraseBytes)
		if err := b.ext.random(raw); err != nil {
			return "", err
		}
		passphrase = hex.EncodeToString(raw)
	}
	salt := make([]byte, 32)
	if err := b.ext.random(salt); err != nil {
		return "", err
	}
	check, err := json.Marshal(passphraseCheck{Salt: hex.EncodeToString(salt), HMAC: hex.EncodeToString(checkMAC(passphrase, salt))})
	if err != nil {
		return "", err
	}
	if err := cloud.PutObject(ctx, account, StateBucket, PassphraseCheck, check, nil); err != nil {
		return "", err
	}
	if given != "" {
		logf("passphrase: %s, checked from now on by %s", PassphraseEnv, PassphraseCheck)
		return passphrase, nil
	}
	logf("passphrase: generated, and printed once")
	_, err = fmt.Fprintf(reveal, `The passphrase of account %s's state, shown this once:

  %s=%s

Keep it in your password manager, and set it in the shell that runs the
stack commands. stack bootstrap --github stores it in CI's secrets. The
state cannot be read without it.

`, account, PassphraseEnv, passphrase)
	return passphrase, err
}

// secretsStore returns the id of the account's Secrets Store: the one the
// values name, else the one bootstrap names, else the account's only one,
// else one it creates.
func (b bootstrapper) secretsStore(ctx context.Context, cloud Cloud, v accountValues, logf func(string, ...any)) (string, error) {
	stores, err := cloud.SecretsStores(ctx, v.accountID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, s := range stores {
		names = append(names, s.Name+" ("+s.ID+")")
	}
	if v.secretsStoreID != "" {
		for _, s := range stores {
			if s.ID == v.secretsStoreID {
				logf("Secrets Store %s (%s): exists", s.Name, s.ID)
				return s.ID, nil
			}
		}
		return "", fmt.Errorf("cloudflare: secretsStoreId %s is no Secrets Store of account %s (its stores: %s)", v.secretsStoreID, v.accountID, orNone(names))
	}
	var chosen *SecretsStore
	for i, s := range stores {
		if s.Name == secretsStoreName {
			chosen = &stores[i]
		}
	}
	switch {
	case chosen == nil && len(stores) == 1:
		chosen = &stores[0]
	case chosen == nil && len(stores) > 1:
		return "", fmt.Errorf("cloudflare: account %s has several Secrets Stores, %s; set secretsStoreId to the one the stack keeps its secrets in", v.accountID, strings.Join(names, ", "))
	}
	if chosen != nil {
		logf("Secrets Store %s (%s): exists", chosen.Name, chosen.ID)
		return chosen.ID, nil
	}
	id, err := cloud.CreateSecretsStore(ctx, v.accountID, secretsStoreName)
	if err != nil {
		return "", err
	}
	logf("Secrets Store %s (%s): created", secretsStoreName, id)
	return id, nil
}

// ciSecret is one GitHub Actions secret --github sets.
type ciSecret struct {
	name, environment string

	// role is the token's role, deployer or planner, or empty for the
	// passphrase.
	role string
}

// ciSecretsFor are the secrets the generated workflow reads for env: a
// plain environment's planner and deployer, a parameterized one's deployer
// alone, since a member is only previewed.
func ciSecretsFor(env *ir.ResolvedEnvironment) []ciSecret {
	roles := []registry.CIRole{registry.CIPlanner, registry.CIDeployer}
	if len(env.Parameters) > 0 {
		roles = roles[1:]
	}
	var out []ciSecret
	for _, role := range roles {
		s := ciSecretsOf(env, role)
		out = append(out,
			ciSecret{name: s.token, environment: s.environment, role: string(role)},
			ciSecret{name: s.passphrase, environment: s.environment},
		)
	}
	return out
}

// storeCI reads the CI's tokens from the bootstrap graph's state and sets
// them and the passphrase as the repository's Actions secrets through
// GitHub. When it cannot, it prints the commands and each token once.
func (b bootstrapper) storeCI(ctx context.Context, req registry.BootstrapRequest, run registry.ProvisionRequest, passphrase string, logf func(string, ...any)) error {
	reader, ok := req.Provisioner.(registry.SecretOutputReader)
	if !ok {
		return fmt.Errorf("cloudflare: --github reads the CI's tokens from the bootstrap's state, and provisioner %s reads no secret output", req.Environment.Provisioner)
	}
	tokens := map[string]string{}
	for _, role := range []string{deployerNode, plannerNode} {
		value, err := reader.SecretOutput(ctx, run, ir.Output{Resource: role, Name: "value"})
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("cloudflare: token %s has no value in the bootstrap's state", tokenName(req.Environment.Stack, role))
		}
		tokens[role] = value
	}
	value := func(s ciSecret) []byte {
		if s.role == "" {
			return []byte(passphrase)
		}
		return []byte(tokens[s.role])
	}
	secrets := ciSecretsFor(req.Environment)
	gh := b.ext.github()
	if err := gh.Ready(ctx); err != nil {
		return b.printCI(req, secrets, tokens, err, logf)
	}
	environments := map[string]bool{}
	for _, s := range secrets {
		if s.environment == "" || environments[s.environment] {
			continue
		}
		environments[s.environment] = true
		created, err := gh.EnsureEnvironment(ctx, req.Repository, s.environment)
		if err != nil {
			return err
		}
		logf("GitHub environment %s of %s: %s", s.environment, req.Repository, createdOrKept(created))
	}
	for _, s := range secrets {
		if err := gh.SetSecret(ctx, req.Repository, s.environment, s.name, value(s)); err != nil {
			return err
		}
		logf("set %s, %s", s.name, scopeOf(req.Repository, s.environment))
	}
	return nil
}

// printCI prints, to the request's Reveal, the gh commands that set the
// CI's secrets, and each token once: the passphrase the person holds
// already.
func (b bootstrapper) printCI(req registry.BootstrapRequest, secrets []ciSecret, tokens map[string]string, why error, logf func(string, ...any)) error {
	if req.Reveal == nil {
		return fmt.Errorf("cloudflare: %w; and bootstrap has nowhere to print the CI's tokens", why)
	}
	logf("%v: printing the commands that set the CI's secrets", why)
	var text strings.Builder
	fmt.Fprintf(&text, "%v.\nSet the generated workflow's secrets yourself: each command below reads the\nvalue from its standard input. Each token is shown this once.\n", why)
	for _, role := range []string{deployerNode, plannerNode} {
		var cmds []string
		for _, s := range secrets {
			if s.role == role {
				cmds = append(cmds, "gh "+strings.Join(setSecretArgs(req.Repository, s.environment, s.name), " "))
			}
		}
		if len(cmds) == 0 {
			continue
		}
		fmt.Fprintf(&text, "\nThe %s token, %s:\n\n  %s\n\n", role, tokenName(req.Environment.Stack, role), tokens[role])
		for _, cmd := range cmds {
			fmt.Fprintf(&text, "  %s\n", cmd)
		}
	}
	fmt.Fprintf(&text, "\nThe state's passphrase, the one in %s:\n\n", PassphraseEnv)
	var envs []string
	for _, s := range secrets {
		if s.role == "" {
			fmt.Fprintf(&text, "  gh %s\n", strings.Join(setSecretArgs(req.Repository, s.environment, s.name), " "))
		}
		if s.environment != "" && !slices.Contains(envs, s.environment) {
			envs = append(envs, s.environment)
		}
	}
	for _, env := range envs {
		fmt.Fprintf(&text, "\nGitHub environment %s must exist first: gh api --method PUT repos/%s/environments/%s\n", env, req.Repository, env)
	}
	text.WriteString("\n")
	_, err := io.WriteString(req.Reveal, text.String())
	return err
}

// scopeOf says where a secret is: a GitHub environment of the repository,
// or the repository.
func scopeOf(repository, environment string) string {
	if environment == "" {
		return "a secret of repository " + repository
	}
	return "a secret of GitHub environment " + environment + " of " + repository
}

func createdOrKept(created bool) string {
	if created {
		return "created"
	}
	return "exists"
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
