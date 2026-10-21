package cloudflare

import (
	"context"
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Secrets live in the account's Secrets Store (section 18.4), one secret
// per secret identity per environment. A secret exists apart from any
// Worker, so it is no node of the graph, and `stack secrets set` writes it
// before or after a deploy. The store reads names, never values: Cloudflare
// gives a value only to a Worker that binds it.

// SecretName is the Secrets Store name of an environment's secret: the
// stack, the environment, the declaring type and the field joined with
// hyphens (`Shop-Staging-PaymentsSecrets-STRIPE_KEY`). The members of a
// parameterized environment share their environment's. An id that holds no
// dot, a platform credential's, names its secret as it is.
func SecretName(stack, environment, id string) string {
	if !strings.Contains(id, ".") {
		return id
	}
	return stack + "-" + environment + "-" + strings.ReplaceAll(id, ".", "-")
}

// secretStore keeps an environment's secret values in the Secrets Store
// bootstrap recorded as secretsStoreId.
type secretStore struct{ ext Extension }

var _ registry.SecretStore = secretStore{}

// open returns the Cloud, the account and the store of env.
func (s secretStore) open(env *ir.ResolvedEnvironment) (Cloud, accountValues, error) {
	v, err := envValues(env)
	if err != nil {
		return nil, v, err
	}
	if v.secretsStoreID == "" {
		return nil, v, fmt.Errorf("cloudflare: environment %s names no secretsStoreId; run stack bootstrap %s, which finds or creates the account's Secrets Store and records its id beside accountId", env.Environment, env.Environment)
	}
	cloud, err := s.ext.cloud()
	return cloud, v, err
}

// find returns the secret of the store named name, if it has one.
func find(ctx context.Context, cloud Cloud, v accountValues, name string) (*Secret, error) {
	secrets, err := cloud.Secrets(ctx, v.accountID, v.secretsStoreID, name)
	if err != nil {
		return nil, err
	}
	for _, secret := range secrets {
		if secret.Name == name && secret.Status != "deleted" {
			return &secret, nil
		}
	}
	return nil, nil
}

func (s secretStore) Exists(ctx context.Context, env *ir.ResolvedEnvironment, id string) (bool, error) {
	cloud, v, err := s.open(env)
	if err != nil {
		return false, err
	}
	secret, err := find(ctx, cloud, v, SecretName(env.Stack, env.Environment, id))
	return secret != nil, err
}

// List reads the store's names once, and returns the ids of env's secrets
// and credentials among them.
func (s secretStore) List(ctx context.Context, env *ir.ResolvedEnvironment) ([]string, error) {
	cloud, v, err := s.open(env)
	if err != nil {
		return nil, err
	}
	prefix := env.Stack + "-" + env.Environment + "-"
	secrets, err := cloud.Secrets(ctx, v.accountID, v.secretsStoreID, prefix)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, secret := range secrets {
		if secret.Status != "deleted" {
			names[secret.Name] = true
		}
	}
	var ids []string
	for _, secret := range env.Secrets {
		if names[SecretName(env.Stack, env.Environment, secret.ID)] {
			ids = append(ids, secret.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Set creates the secret, or replaces its value. It needs no deploy first.
func (s secretStore) Set(ctx context.Context, env *ir.ResolvedEnvironment, id string, value []byte) error {
	cloud, v, err := s.open(env)
	if err != nil {
		return err
	}
	name := SecretName(env.Stack, env.Environment, id)
	secret, err := find(ctx, cloud, v, name)
	if err != nil {
		return err
	}
	if secret != nil {
		return cloud.UpdateSecret(ctx, v.accountID, v.secretsStoreID, secret.ID, value)
	}
	return cloud.CreateSecret(ctx, v.accountID, v.secretsStoreID, name, value)
}

// Get refuses: the Secrets Store gives a value to a Worker that binds it
// alone, and no cloudflare environment has a platform credential for the
// deploy to read.
func (s secretStore) Get(_ context.Context, env *ir.ResolvedEnvironment, id string) ([]byte, error) {
	return nil, fmt.Errorf("cloudflare: the Secrets Store gives secret %s of environment %s only to a Worker that binds it; nothing reads it back", id, env.Environment)
}
