package gcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The gcp target's deploy state and secret store (docs/stack-model.md,
// sections 4.2, 7.3 and 11.2). Bootstrap creates, in each environment's
// project, a state bucket and a Cloud KMS key; Pulumi keeps its state in
// the bucket, encrypted with the key, and the deploy manifests sit beside
// it. The bucket keeps every version of an object, so a manifest a deploy
// replaced, or a state file, can be read back.

// The names bootstrap gives what it creates directly.
const (
	// stateKeyRing and stateKey are the KMS key ring and key that encrypt
	// the secrets in Pulumi's state, in the environment's region.
	stateKeyRing = "superschematic"
	stateKey     = "pulumi-state"

	// manifestPrefix is where the deploy manifests sit in the bucket.
	manifestPrefix = "superschematic/manifests/"
)

// stateBucket is the state bucket of a project: the project id, which is
// unique, and a fixed suffix.
func stateBucket(project string) string { return project + "-superschematic-state" }

// stateKeyName is the resource name of the state key.
func stateKeyName(v values) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s/cryptoKeys/%s", v.project, v.region, stateKeyRing, stateKey)
}

// envValues reads the gcp values of a resolved environment.
func envValues(env *ir.ResolvedEnvironment) (values, error) {
	v := valuesOf(registry.StackEnvironment{Values: env.Values})
	if v.project == "" || v.region == "" {
		return v, fmt.Errorf("gcp: environment %s sets no project or region", env.Environment)
	}
	return v, nil
}

// cloud returns the extension's Cloud, or the client libraries'.
func (e Extension) cloud() Cloud {
	if e.Cloud != nil {
		return e.Cloud
	}
	return defaultCloud()
}

// stateStore keeps Pulumi's state and the deploy manifests in the state
// bucket.
type stateStore struct{ ext Extension }

var _ registry.StateStore = stateStore{}

// Backend is the state bucket, with the KMS key as Pulumi's secrets
// provider.
func (s stateStore) Backend(_ context.Context, env *ir.ResolvedEnvironment) (registry.StateBackend, error) {
	v, err := envValues(env)
	if err != nil {
		return registry.StateBackend{}, err
	}
	return registry.StateBackend{
		URL:             "gs://" + stateBucket(v.project),
		SecretsProvider: "gcpkms://" + stateKeyName(v),
	}, nil
}

// manifestObject is where a run's manifest sits in the bucket.
func manifestObject(run registry.Run) string {
	return manifestPrefix + kebab(run.Environment.Stack) + "/" + run.Name() + ".json"
}

func (s stateStore) ReadManifest(ctx context.Context, run registry.Run) ([]byte, error) {
	v, err := envValues(run.Environment)
	if err != nil {
		return nil, err
	}
	data, err := s.ext.cloud().ReadObject(ctx, stateBucket(v.project), manifestObject(run))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", run.Name(), registry.ErrNoManifest)
	}
	return data, err
}

func (s stateStore) WriteManifest(ctx context.Context, run registry.Run, data []byte) error {
	v, err := envValues(run.Environment)
	if err != nil {
		return err
	}
	return s.ext.cloud().WriteObject(ctx, stateBucket(v.project), manifestObject(run), data)
}

func (s stateStore) DeleteManifest(ctx context.Context, run registry.Run) error {
	v, err := envValues(run.Environment)
	if err != nil {
		return err
	}
	return s.ext.cloud().DeleteObject(ctx, stateBucket(v.project), manifestObject(run))
}

// secretStore keeps secret values in Secret Manager, in the environment's
// project. An application secret's ID (`PaymentsSecrets.STRIPE_KEY`)
// names the secret its graph node creates (secretName); a credential's ID
// is its secret's name, which holds no dot. The secret itself is created
// by the deploy's infrastructure step for an application secret, and by
// bootstrap for a credential; the store adds versions.
type secretStore struct{ ext Extension }

var _ registry.SecretStore = secretStore{}

// storedName is the Secret Manager name of the secret id names.
func storedName(env *ir.ResolvedEnvironment, id string) string {
	if strings.Contains(id, ".") {
		return secretName(env.Stack, id)
	}
	return id
}

func (s secretStore) Exists(ctx context.Context, env *ir.ResolvedEnvironment, id string) (bool, error) {
	v, err := envValues(env)
	if err != nil {
		return false, err
	}
	return s.ext.cloud().SecretHasValue(ctx, v.project, storedName(env, id))
}

func (s secretStore) List(ctx context.Context, env *ir.ResolvedEnvironment) ([]string, error) {
	var ids []string
	for _, secret := range env.Secrets {
		ok, err := s.Exists(ctx, env, secret.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, secret.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (s secretStore) Set(ctx context.Context, env *ir.ResolvedEnvironment, id string, value []byte) error {
	v, err := envValues(env)
	if err != nil {
		return err
	}
	return s.ext.cloud().AddSecretVersion(ctx, v.project, storedName(env, id), value)
}

func (s secretStore) Get(ctx context.Context, env *ir.ResolvedEnvironment, id string) ([]byte, error) {
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	return s.ext.cloud().AccessSecret(ctx, v.project, storedName(env, id))
}
