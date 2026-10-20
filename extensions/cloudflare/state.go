package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The cloudflare target's deploy state (section 18.3): Pulumi keeps its
// state in StateBucket, one R2 bucket per account, through its S3 backend,
// and encrypts the secrets in it with the passphrase bootstrap generated,
// which whoever deploys keeps. The deploy manifests sit beside it. R2
// keeps no versions, so each manifest is written twice: the current one,
// and a copy under the time it was written.

// cloud returns the extension's Cloud, or one over the REST and S3 APIs
// with the token in the shell.
func (e Extension) cloud() (Cloud, error) {
	if e.Cloud != nil {
		return e.Cloud, nil
	}
	token := e.getenv(TokenEnv)
	if token == "" {
		return nil, errNoToken
	}
	return NewCloud(token), nil
}

// errNoToken refuses a call to Cloudflare with no token in the shell.
var errNoToken = fmt.Errorf("cloudflare: set %s to an API token of the account; stack bootstrap --github sets the CI's", TokenEnv)

// stateStore keeps Pulumi's state and the deploy manifests in StateBucket.
type stateStore struct{ ext Extension }

var _ registry.StateStore = stateStore{}

// Backend is StateBucket through Pulumi's S3 backend on R2, with the
// passphrase secrets provider. It verifies the shell's token, whose id and
// value give the S3 credentials, and hands the run those and the shell's
// passphrase in Env. A run without a token or a passphrase is refused here,
// before the provisioner starts.
func (s stateStore) Backend(ctx context.Context, env *ir.ResolvedEnvironment) (registry.StateBackend, error) {
	v, err := envValues(env)
	if err != nil {
		return registry.StateBackend{}, err
	}
	passphrase := s.ext.getenv(PassphraseEnv)
	if passphrase == "" {
		return registry.StateBackend{}, fmt.Errorf("cloudflare: set %s to the passphrase of account %s's state: stack bootstrap %s generated it on its first run and printed it once, and it sits in your password manager and in CI's secrets", PassphraseEnv, v.accountID, env.Environment)
	}
	token := s.ext.getenv(TokenEnv)
	if token == "" {
		return registry.StateBackend{}, errNoToken
	}
	cloud, err := s.ext.cloud()
	if err != nil {
		return registry.StateBackend{}, err
	}
	tok, err := cloud.VerifyToken(ctx, v.accountID)
	if err != nil {
		return registry.StateBackend{}, err
	}
	return stateBackend(v.accountID, tok.ID, token, passphrase), nil
}

// stateBackend is the backend of an account's state, reached with a
// token's S3 credentials and encrypted with passphrase.
func stateBackend(account, tokenID, token, passphrase string) registry.StateBackend {
	return registry.StateBackend{
		URL:             StateURL(R2Endpoint(account)),
		SecretsProvider: passphraseProvider,
		Env:             s3Env(tokenID, token, map[string]string{PassphraseEnv: passphrase}),
	}
}

// s3Env is the environment that hands Pulumi's S3 backend a token's S3
// credentials and nothing else of the shell's AWS setup, beside extra: a
// session token or a profile there would be signed with or read in place
// of them (an AWS session token beside R2's key fails every request, and a
// profile the machine lacks fails the backend's start), so each is
// cleared, and the shared config and credentials files are empty.
func s3Env(tokenID, token string, extra map[string]string) map[string]string {
	id, secret := S3Credentials(tokenID, token)
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":           id,
		"AWS_SECRET_ACCESS_KEY":       secret,
		"AWS_SESSION_TOKEN":           "",
		"AWS_SECURITY_TOKEN":          "",
		"AWS_PROFILE":                 "",
		"AWS_DEFAULT_PROFILE":         "",
		"AWS_CONFIG_FILE":             os.DevNull,
		"AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
	}
	maps.Copy(env, extra)
	return env
}

// passphraseProvider is Pulumi's secrets provider that encrypts with the
// passphrase in PassphraseEnv.
const passphraseProvider = "passphrase"

// manifestObject is where a run's current manifest sits in the bucket.
func manifestObject(run registry.Run) string {
	return ManifestPrefix + kebab(run.Environment.Stack) + "/" + run.Name() + ".json"
}

// historyObject is where the copy of a run's manifest written at a time
// sits: RFC 3339 in UTC to the millisecond, which sorts by time.
func (s stateStore) historyObject(run registry.Run) string {
	stamp := s.ext.now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return ManifestPrefix + kebab(run.Environment.Stack) + "/" + run.Name() + "/" + stamp + ".json"
}

// open returns the Cloud and the account of a run's environment.
func (s stateStore) open(run registry.Run) (Cloud, string, error) {
	v, err := envValues(run.Environment)
	if err != nil {
		return nil, "", err
	}
	cloud, err := s.ext.cloud()
	return cloud, v.accountID, err
}

func (s stateStore) ReadManifest(ctx context.Context, run registry.Run) ([]byte, error) {
	cloud, account, err := s.open(run)
	if err != nil {
		return nil, err
	}
	data, _, err := cloud.GetObject(ctx, account, StateBucket, manifestObject(run))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", run.Name(), registry.ErrNoManifest)
	}
	return data, err
}

// WriteManifest writes the history copy first, then the current one, so
// every manifest that was ever current has its copy.
func (s stateStore) WriteManifest(ctx context.Context, run registry.Run, data []byte) error {
	cloud, account, err := s.open(run)
	if err != nil {
		return err
	}
	if err := cloud.PutObject(ctx, account, StateBucket, s.historyObject(run), data, nil); err != nil {
		return err
	}
	return cloud.PutObject(ctx, account, StateBucket, manifestObject(run), data, nil)
}

// DeleteManifest deletes the current manifest and keeps the history.
func (s stateStore) DeleteManifest(ctx context.Context, run registry.Run) error {
	cloud, account, err := s.open(run)
	if err != nil {
		return err
	}
	return cloud.DeleteObject(ctx, account, StateBucket, manifestObject(run))
}
