package gcp

import (
	"context"
	"encoding/json"
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

	// manifestPrefix is where the deploy manifests sit in the bucket, and
	// lockPrefix where each run's deploy lock does (D46, amended).
	manifestPrefix = "superschematic/manifests/"
	lockPrefix     = "superschematic/locks/"

	// stateRetentionDays is how long the state bucket keeps a build
	// context, a migration job document or a plan (stateLifecycle).
	stateRetentionDays = 30
)

// stateLifecycle is the state bucket's lifecycle rule (D46): it deletes
// the build contexts under buildPrefix, and the job documents and plans
// under migrationPrefix, once they are stateRetentionDays old. A deploy
// writes each just before the build or the execution that reads it, every
// time it needs one, and nothing reads one afterwards: the deploy compares
// a context's digest with the one the manifest records, the builder finds
// an image by its tag, `context-<digest>`, and uploads the context again
// for a build it runs, so a deleted context costs nothing. Pulumi's state,
// under `.pulumi/`, and the deploy manifests, under manifestPrefix, have
// no rule. A site's files sit in the site's own bucket, not this one.
func stateLifecycle() LifecycleRule {
	return LifecycleRule{Prefixes: []string{buildPrefix, migrationPrefix}, AgeDays: stateRetentionDays}
}

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

// The deploy lock of a run (D46, amended) is an object of the state
// bucket, superschematic/locks/<stack>/<run>.json, holding the lock's JSON:
// its ID, the run, the operation, the holder, the host, the process and
// when it was taken. Lock writes it with ifGenerationMatch=0, so Cloud
// Storage creates it for one writer only; Unlock deletes it with
// ifGenerationMatch at the generation it read, after checking the ID, so
// it removes only the lock it named. The bucket keeps versions, so each
// lock a deploy held stays readable as a noncurrent version; the
// precondition reads the live one. `deployer` writes and deletes it with
// the object admin role it holds on the bucket already.

// lockAttempts bounds Lock and Unlock when the lock changes hands between
// their read and their write.
const lockAttempts = 3

// lockObject is where a run's deploy lock sits in the bucket.
func lockObject(run registry.Run) string {
	return lockPrefix + kebab(run.Environment.Stack) + "/" + run.Name() + ".json"
}

var _ registry.StateLocker = stateStore{}

func (s stateStore) Lock(ctx context.Context, run registry.Run, lock registry.DeployLock) error {
	v, err := envValues(run.Environment)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	bucket, object := stateBucket(v.project), lockObject(run)
	cloud := s.ext.cloud()
	for range lockAttempts {
		err := cloud.CreateObject(ctx, bucket, object, data)
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		held, _, err := readLock(ctx, cloud, bucket, object)
		if err != nil {
			return err
		}
		if held != nil {
			return &registry.LockHeldError{Lock: *held}
		}
		// Released between the write and the read: take it again.
	}
	return fmt.Errorf("gcp: the deploy lock of %s changed hands %d times while this deploy took it; deploy again", run.Name(), lockAttempts)
}

func (s stateStore) ReadLock(ctx context.Context, run registry.Run) (*registry.DeployLock, error) {
	v, err := envValues(run.Environment)
	if err != nil {
		return nil, err
	}
	held, _, err := readLock(ctx, s.ext.cloud(), stateBucket(v.project), lockObject(run))
	return held, err
}

func (s stateStore) Unlock(ctx context.Context, run registry.Run, id string) error {
	v, err := envValues(run.Environment)
	if err != nil {
		return err
	}
	bucket, object := stateBucket(v.project), lockObject(run)
	cloud := s.ext.cloud()
	for range lockAttempts {
		held, generation, err := readLock(ctx, cloud, bucket, object)
		switch {
		case err != nil:
			return err
		case held == nil:
			return nil
		case held.ID != id:
			return &registry.LockHeldError{Lock: *held}
		}
		deleted, err := cloud.DeleteObjectGeneration(ctx, bucket, object, generation)
		if err != nil || deleted {
			return err
		}
		// Another version went live between the read and the delete: read
		// it again.
	}
	return fmt.Errorf("gcp: the deploy lock of %s changed %d times while it was released; run `stack unlock` to remove it", run.Name(), lockAttempts)
}

// readLock reads a lock object: the lock and its generation, or nil when
// none is live. An object that holds no lock is refused with how to remove
// it by hand.
func readLock(ctx context.Context, cloud Cloud, bucket, object string) (*registry.DeployLock, int64, error) {
	data, generation, err := cloud.ReadObjectGeneration(ctx, bucket, object)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var held registry.DeployLock
	if err := json.Unmarshal(data, &held); err != nil || held.ID == "" {
		if err == nil {
			err = errors.New("it has no id")
		}
		return nil, 0, fmt.Errorf("gcp: gs://%s/%s holds no deploy lock superschematic reads (%v); once no deploy runs, delete it with `gcloud storage rm gs://%s/%s`", bucket, object, err, bucket, object)
	}
	return &held, generation, nil
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
