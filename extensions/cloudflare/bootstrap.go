package cloudflare

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
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
//     account or a user token, and refuses an account whose bootstrap
//     state an earlier release kept in StateBucket (legacyBootstrapState);
//  2. it creates StateBucket and BootstrapBucket directly, since Pulumi
//     needs them first;
//  3. it generates each bucket's passphrase, 32 random bytes it prints
//     once, or checks the one in the shell against the bucket's
//     PassphraseCheck, a key PBKDF2-SHA256 derives from it, which its
//     first run writes with a conditional write: the state's, in
//     PassphraseEnv, which CI holds, and bootstrap's own, in
//     BootstrapPassphraseEnv, which no CI job holds;
//  4. it finds the account's Secrets Store, or creates it;
//  5. it applies the bootstrap graph in Pulumi project `<stack>-bootstrap`,
//     a stack per account, kept in BootstrapBucket: the `<stack>-deployer`,
//     `<stack>-planner` and `<stack>-preview` account tokens, with the
//     permission groups' ids read from the API first, which also checks
//     the group a bucket edge's token names by its published id
//     (checkBucketTokenGroup);
//  6. with --github, it sets each token the environment's CI jobs read and
//     the state's passphrase as the repository's Actions secrets through
//     gh, at the scopes the CI seam names; without gh, it prints the
//     commands and each token once;
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

// The three tokens' policies (section 18.3). The deployer edits the
// account's Workers scripts, D1, R2, Secrets Store and account tokens,
// since a bucket edge mints a token (section 18.7), and the Workers routes
// and DNS of the account's zones; so it can grant itself more, as gcp's
// deployer can. The planner reads the Workers scripts, D1, the Secrets
// Store's names and the account's tokens, and writes the objects of the
// state bucket alone, since a preview takes the stack's lock: it holds no
// account-wide R2 group, whose read reaches every bucket's objects,
// BootstrapBucket's among them. The preview deploys and destroys a
// parameterized environment's members: it edits Workers scripts, D1, R2,
// whose buckets a member makes, and account tokens, which a member's bucket
// edge mints, and the zones' Workers routes and DNS, and reads the Secrets
// Store, whose values a member's Workers bind and only `stack secrets set`
// writes; revoking it leaves the deployer's and the planner's working. All
// three reach every zone of the account: the graph is one per account, and
// the environments of one account may hold their domains in different
// zones.
var (
	deployerPolicies = []tokenPolicy{
		{reachAccount, []string{"Workers Scripts Write", "D1 Write", "Workers R2 Storage Write", "Secrets Store Write", "Account API Tokens Write"}},
		{reachZones, []string{"Workers Routes Write", "DNS Write"}},
	}
	plannerPolicies = []tokenPolicy{
		{reachAccount, []string{"Workers Scripts Read", "D1 Read", "Secrets Store Read", "Account API Tokens Read"}},
		{reachZones, []string{"Workers Routes Read", "DNS Read"}},
		{reachState, []string{"Workers R2 Storage Bucket Item Write"}},
	}
	previewPolicies = []tokenPolicy{
		{reachAccount, []string{"Workers Scripts Write", "D1 Write", "Workers R2 Storage Write", "Secrets Store Read", "Account API Tokens Write"}},
		{reachZones, []string{"Workers Routes Write", "DNS Write"}},
	}
)

// The bootstrap graph's nodes: the CI's three tokens.
const (
	deployerNode = "deployer"
	plannerNode  = "planner"
	previewNode  = "preview"
)

// bootstrapTokens are the graph's tokens, each with its policies, in the
// order they are applied and printed.
var bootstrapTokens = []struct {
	id       string
	policies []tokenPolicy
}{{deployerNode, deployerPolicies}, {plannerNode, plannerPolicies}, {previewNode, previewPolicies}}

// tokenName is the name of one of the stack's CI tokens:
// `<stack>-deployer`, `<stack>-planner`, `<stack>-preview`. A token is the
// stack's in its account, not an environment's, so the name holds no
// environment.
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
// Pulumi stack per account, kept in BootstrapBucket. groups are the
// permission groups the API lists, whose ids the tokens' policies name.
func BootstrapEnvironment(env *ir.ResolvedEnvironment, groups []PermissionGroup) (*ir.ResolvedEnvironment, error) {
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	var nodes []*ir.Resource
	var ids []string
	for _, role := range bootstrapTokens {
		var policies []any
		for _, p := range role.policies {
			resources, err := reachResources(p.reach, v.accountID)
			if err != nil {
				return nil, err
			}
			var groupIDs []any
			for _, name := range p.groups {
				id, err := groupID(groups, name, reachScopes[p.reach])
				if err != nil {
					return nil, err
				}
				groupIDs = append(groupIDs, map[string]any{"id": id})
			}
			policies = append(policies, map[string]any{"effect": "allow", "permissionGroups": groupIDs, "resources": resources})
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
		ids = append(ids, role.id)
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
		DeployOrder: []*ir.DeployStep{{Step: ir.StepInfrastructure, Resources: ids}},
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

	// 2. The buckets. An earlier release kept the bootstrap's state in the
	// state bucket, where the planner's token reaches it.
	for _, bucket := range []string{StateBucket, BootstrapBucket} {
		created, err := cloud.EnsureBucket(ctx, account, bucket)
		if err != nil {
			return nil, err
		}
		logf("bucket %s: %s", bucket, createdOrKept(created))
	}
	if err := legacyBootstrapState(ctx, cloud, account, env.Stack); err != nil {
		return nil, err
	}

	// 3. The passphrases: the state's, then the bootstrap's own.
	passphrase, err := b.passphrase(ctx, cloud, account, statePassphrase, logf, req.Reveal)
	if err != nil {
		return nil, err
	}
	bootstrapPassphrase, err := b.passphrase(ctx, cloud, account, bootstrapStatePassphrase, logf, req.Reveal)
	if err != nil {
		return nil, err
	}

	// 4. The Secrets Store.
	store, err := b.secretsStore(ctx, cloud, v, logf)
	if err != nil {
		return nil, err
	}

	// 5. The bootstrap graph, in the bootstrap bucket under the
	// bootstrap's own passphrase.
	groups, err := cloud.PermissionGroups(ctx, account)
	if err != nil {
		return nil, err
	}
	if err := checkBucketTokenGroup(env, groups); err != nil {
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
	run := registry.ProvisionRequest{Environment: graph, Dir: dir, Backend: bootstrapBackend(account, tok.ID, token, bootstrapPassphrase)}
	logf("apply the bootstrap graph in %s: tokens %s, %s and %s", BootstrapBucket, tokenName(env.Stack, deployerNode), tokenName(env.Stack, plannerNode), tokenName(env.Stack, previewNode))
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
		logf("the CI's tokens are in the bootstrap's state; stack bootstrap %s --github sets them, and the state's passphrase, as the repository's Actions secrets", env.Environment)
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

// bootstrapBackend is the backend of the bootstrap graph's state: the
// bootstrap bucket, reached with a token's S3 credentials, its secrets
// encrypted with the bootstrap's own passphrase, which the provisioner
// reads from PassphraseEnv.
func bootstrapBackend(account, tokenID, token, passphrase string) registry.StateBackend {
	return registry.StateBackend{
		URL:             BootstrapURL(R2Endpoint(account)),
		SecretsProvider: passphraseProvider,
		Env:             s3Env(tokenID, token, map[string]string{PassphraseEnv: passphrase}),
	}
}

// pulumiStacks is where Pulumi's S3 backend keeps a project's stacks,
// `.pulumi/stacks/<project>/<stack>.json`, which its history, backups and
// locks sit beside under `.pulumi/`.
const pulumiStacks = ".pulumi/stacks/"

// bootstrapProject is the Pulumi project of a stack's bootstrap graph, as
// the provisioner names the graph's stack, `<Stack>Bootstrap`.
func bootstrapProject(stack string) string { return kebab(stack + "Bootstrap") }

// legacyBootstrapState refuses an account whose bootstrap state for stack
// sits in StateBucket, where an earlier release kept it: the planner's
// token wrote that bucket's objects and CI held the passphrase of its
// secrets, so whoever held the planner's token could read the deployer's.
// Bootstrap moves nothing: the tokens it holds are to be revoked, and a
// bootstrap after the cleanup mints new ones in BootstrapBucket.
func legacyBootstrapState(ctx context.Context, cloud Cloud, account, stack string) error {
	project := bootstrapProject(stack)
	keys, err := cloud.ListObjects(ctx, account, StateBucket, pulumiStacks+project+"/")
	if err != nil || len(keys) == 0 {
		return err
	}
	return fmt.Errorf("cloudflare: account %s keeps stack %s's bootstrap state in %s (%s), where an earlier release put it and where the planner's token reaches it; "+
		"this release keeps it in %s, which no CI job can decrypt. The tokens that state holds were readable with the planner's: "+
		"delete %s, %s and %s in the dashboard (Manage Account, Account API Tokens); delete every object of %s under .pulumi/stacks/%s/, .pulumi/history/%s/, .pulumi/backups/%s/ and .pulumi/locks/ that names %s; "+
		"then run stack bootstrap again, which mints new tokens there, and with --github sets them",
		account, stack, StateBucket, strings.Join(keys, ", "), BootstrapBucket,
		tokenName(stack, deployerNode), tokenName(stack, plannerNode), tokenName(stack, previewNode),
		StateBucket, project, project, project, project)
}

// passphraseKind is a passphrase bootstrap keeps a check of: the state's,
// which CI holds, and the bootstrap's own, which no CI job holds.
type passphraseKind struct {
	// env is the shell's variable that holds it, and bucket the bucket
	// whose state it encrypts and whose PassphraseCheck checks it.
	env, bucket string

	// what names the state it encrypts, and keep where the person keeps
	// it, as the reveal says.
	what, keep string
}

var (
	statePassphrase = passphraseKind{
		env: PassphraseEnv, bucket: StateBucket, what: "state",
		keep: "Keep it in your password manager, and set it in the shell that runs the\nstack commands. stack bootstrap --github stores it in CI's secrets.",
	}
	bootstrapStatePassphrase = passphraseKind{
		env: BootstrapPassphraseEnv, bucket: BootstrapBucket, what: "bootstrap's state",
		keep: "Keep it in your password manager, and set it in the shell that runs\nstack bootstrap. It encrypts the CI's tokens in the bootstrap's own state,\nand no CI job holds it.",
	}
)

// The passphrase check (PassphraseCheck): a key PBKDF2-SHA256 derives from
// the passphrase over a random salt, with its iteration count, each
// written beside it. It is stretched so that a reader of the object, the
// deployer's or the preview's token for BootstrapBucket's, the planner's
// for StateBucket's, cannot cheaply try passphrases against it.
type passphraseCheck struct {
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Key        string `json:"key"`
}

const (
	checkKDF = "pbkdf2-sha256"

	// checkIterations is the iteration count a check is written with, the
	// count OWASP recommends for PBKDF2-HMAC-SHA256; a check is read with
	// its own, within minCheckIterations and maxCheckIterations.
	checkIterations    = 600_000
	minCheckIterations = 100_000
	maxCheckIterations = 10_000_000

	checkSaltBytes = 32
	checkKeyBytes  = 32
)

// newCheck writes passphrase's check over salt.
func newCheck(passphrase string, salt []byte) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, checkIterations, checkKeyBytes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(passphraseCheck{KDF: checkKDF, Iterations: checkIterations, Salt: hex.EncodeToString(salt), Key: hex.EncodeToString(key)})
}

// matchesCheck reports whether passphrase is the one data checks, and
// refuses data that is no check this release reads.
func matchesCheck(data []byte, passphrase string, kind passphraseKind) (bool, error) {
	var check passphraseCheck
	bad := func(why string) error {
		return fmt.Errorf("cloudflare: %s in %s is no passphrase check this release reads (%s); if an earlier release wrote it, delete it and run stack bootstrap again with %s set to the passphrase, which writes it anew", PassphraseCheck, kind.bucket, why, kind.env)
	}
	if err := json.Unmarshal(data, &check); err != nil {
		return false, bad(err.Error())
	}
	salt, saltErr := hex.DecodeString(check.Salt)
	want, keyErr := hex.DecodeString(check.Key)
	switch {
	case check.KDF != checkKDF:
		return false, bad(fmt.Sprintf("kdf %q, not %s", check.KDF, checkKDF))
	case check.Iterations < minCheckIterations || check.Iterations > maxCheckIterations:
		return false, bad(fmt.Sprintf("%d iterations, not %d to %d", check.Iterations, minCheckIterations, maxCheckIterations))
	case saltErr != nil || keyErr != nil || len(salt) == 0 || len(want) == 0:
		return false, bad("its salt or its key is not hex")
	}
	got, err := pbkdf2.Key(sha256.New, passphrase, salt, check.Iterations, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// passphrase returns the passphrase of kind. With a check in its bucket it
// checks the shell's against it, and refuses a missing or wrong one.
// Without, this is the account's first bootstrap: it takes the shell's
// passphrase, or generates one, unless the bucket holds Pulumi state
// already, which a new passphrase would not decrypt; it writes the check
// only if none was written meanwhile, reads the check back, and refuses
// one another bootstrap wrote at the same time for another passphrase.
// Only then does it print a generated passphrase, once, to reveal.
func (b bootstrapper) passphrase(ctx context.Context, cloud Cloud, account string, kind passphraseKind, logf func(string, ...any), reveal io.Writer) (string, error) {
	given := b.ext.getenv(kind.env)
	data, _, err := cloud.GetObject(ctx, account, kind.bucket, PassphraseCheck)
	switch {
	case err == nil:
		if given == "" {
			return "", fmt.Errorf("cloudflare: account %s's %s has a passphrase, which its first bootstrap printed; set %s to it", account, kind.what, kind.env)
		}
		ok, err := matchesCheck(data, given, kind)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("cloudflare: %s is not the passphrase of account %s's %s, which its first bootstrap printed", kind.env, account, kind.what)
		}
		logf("passphrase of the %s: %s matches the check in %s", kind.what, kind.env, kind.bucket)
		return given, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	passphrase := given
	if passphrase == "" {
		if reveal == nil {
			return "", fmt.Errorf("cloudflare: bootstrap generates the passphrase of account %s's %s and prints it once, and has nowhere to print it; set %s to a passphrase of your own", account, kind.what, kind.env)
		}
		stacks, err := cloud.ListObjects(ctx, account, kind.bucket, pulumiStacks)
		if err != nil {
			return "", err
		}
		if len(stacks) > 0 {
			return "", fmt.Errorf("cloudflare: %s holds Pulumi state and no %s, so bootstrap would generate a passphrase that does not decrypt it; set %s to the passphrase the state was written with, and bootstrap writes its check", kind.bucket, PassphraseCheck, kind.env)
		}
		raw := make([]byte, passphraseBytes)
		if err := b.ext.random(raw); err != nil {
			return "", err
		}
		passphrase = hex.EncodeToString(raw)
	}
	salt := make([]byte, checkSaltBytes)
	if err := b.ext.random(salt); err != nil {
		return "", err
	}
	check, err := newCheck(passphrase, salt)
	if err != nil {
		return "", err
	}
	created, err := cloud.CreateObject(ctx, account, kind.bucket, PassphraseCheck, check)
	if err != nil {
		return "", err
	}
	back, _, err := cloud.GetObject(ctx, account, kind.bucket, PassphraseCheck)
	if err != nil {
		return "", fmt.Errorf("cloudflare: read back %s in %s: %w", PassphraseCheck, kind.bucket, err)
	}
	ok, err := matchesCheck(back, passphrase, kind)
	if err != nil {
		return "", err
	}
	switch {
	case !ok && !created:
		return "", fmt.Errorf("cloudflare: another bootstrap of account %s wrote the check of its %s's passphrase in %s at the same time, for another passphrase; run stack bootstrap again with %s set to the passphrase that bootstrap printed", account, kind.what, kind.bucket, kind.env)
	case !ok:
		return "", fmt.Errorf("cloudflare: %s in %s does not check the passphrase bootstrap wrote it for", PassphraseCheck, kind.bucket)
	case !created:
		logf("passphrase of the %s: another bootstrap wrote the same check in %s", kind.what, kind.bucket)
	}
	if given != "" {
		logf("passphrase of the %s: %s, checked from now on by %s in %s", kind.what, kind.env, PassphraseCheck, kind.bucket)
		return passphrase, nil
	}
	logf("passphrase of the %s: generated, and printed once", kind.what)
	_, err = fmt.Fprintf(reveal, `The passphrase of account %s's %s, shown this once:

  %s=%s

%s
The %s cannot be read without it.

`, account, kind.what, kind.env, passphrase, kind.keep, kind.what)
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

	// token is the bootstrap graph's token the secret holds, deployer,
	// planner or preview, or empty for the state's passphrase.
	token string
}

// ciSecretsFor are the secrets the generated workflow reads for env: a
// plain environment's plan job's, the planner's token, and its deploy
// job's, the deployer's; a parameterized one's preview job's alone, the
// preview's token, since a member is only previewed. Each comes with the
// state's passphrase.
func ciSecretsFor(env *ir.ResolvedEnvironment) []ciSecret {
	type job struct {
		role  registry.CIRole
		token string
	}
	jobs := []job{{registry.CIPlanner, plannerNode}, {registry.CIDeployer, deployerNode}}
	if len(env.Parameters) > 0 {
		jobs = []job{{registry.CIDeployer, previewNode}}
	}
	var out []ciSecret
	for _, j := range jobs {
		s := ciSecretsOf(env, j.role)
		out = append(out,
			ciSecret{name: s.token, environment: s.environment, token: j.token},
			ciSecret{name: s.passphrase, environment: s.environment},
		)
	}
	return out
}

// storeCI reads the tokens env's CI jobs read from the bootstrap graph's
// state and sets them and the state's passphrase as the repository's
// Actions secrets through GitHub. When it cannot, it prints the commands
// and each token once. The bootstrap's own passphrase is never set.
func (b bootstrapper) storeCI(ctx context.Context, req registry.BootstrapRequest, run registry.ProvisionRequest, passphrase string, logf func(string, ...any)) error {
	reader, ok := req.Provisioner.(registry.SecretOutputReader)
	if !ok {
		return fmt.Errorf("cloudflare: --github reads the CI's tokens from the bootstrap's state, and provisioner %s reads no secret output", req.Environment.Provisioner)
	}
	secrets := ciSecretsFor(req.Environment)
	tokens := map[string]string{}
	for _, s := range secrets {
		if s.token == "" || tokens[s.token] != "" {
			continue
		}
		value, err := reader.SecretOutput(ctx, run, ir.Output{Resource: s.token, Name: "value"})
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("cloudflare: token %s has no value in the bootstrap's state", tokenName(req.Environment.Stack, s.token))
		}
		tokens[s.token] = value
	}
	value := func(s ciSecret) []byte {
		if s.token == "" {
			return []byte(passphrase)
		}
		return []byte(tokens[s.token])
	}
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
	for _, role := range bootstrapTokens {
		var cmds []string
		for _, s := range secrets {
			if s.token == role.id {
				cmds = append(cmds, "gh "+strings.Join(setSecretArgs(req.Repository, s.environment, s.name), " "))
			}
		}
		if len(cmds) == 0 {
			continue
		}
		fmt.Fprintf(&text, "\nThe %s token, %s:\n\n  %s\n\n", role.id, tokenName(req.Environment.Stack, role.id), tokens[role.id])
		for _, cmd := range cmds {
			fmt.Fprintf(&text, "  %s\n", cmd)
		}
	}
	fmt.Fprintf(&text, "\nThe state's passphrase, the one in %s:\n\n", PassphraseEnv)
	var envs []string
	for _, s := range secrets {
		if s.token == "" {
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
