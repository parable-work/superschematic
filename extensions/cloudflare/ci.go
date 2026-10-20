package cloudflare

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// CIIdentityKind is the kind of the cloudflare target's CI identities: an
// account token bootstrap's graph made and the state's passphrase, each a
// GitHub Actions secret, whose names the fields `token` and `passphrase`
// give; `environment`, when set, names the GitHub environment that holds
// them, else the repository does. The core's GitHub Actions renderer reads
// both into the env of the steps that run the stack's commands, with no
// action (section 18.3, D64). It is the
// first CI identity that is a long-lived secret: Cloudflare offers no
// federation.
const CIIdentityKind = "cloudflare-api-token"

// ciSecrets are the secrets a CI job of an environment reads as one role.
type ciSecrets struct {
	token, passphrase string

	// environment is the GitHub environment that holds them; empty is the
	// repository.
	environment string
}

// ciSecretsOf names the secrets a job reads (section 18.3), each name
// ending with the stack's name and the environment's, in upper case with
// their words joined by underscores, so two stacks of one repository, on
// one account or two, never set each other's: a plain environment's deploy
// job reads CLOUDFLARE_DEPLOYER_TOKEN_<STACK>_<ENV> and
// PULUMI_CONFIG_PASSPHRASE_<STACK>_<ENV> from the GitHub environment of its
// name, whose reviewers approve it; its plan job reads the planner's token
// and the passphrase as repository secrets, as a parameterized
// environment's preview job reads the preview's, since a pull request's
// job runs in no GitHub environment.
func ciSecretsOf(env *ir.ResolvedEnvironment, role registry.CIRole) ciSecrets {
	suffix := "_" + screaming(env.Stack) + "_" + screaming(env.Environment)
	passphrase := PassphraseEnv + suffix
	switch {
	case role == registry.CIPlanner:
		return ciSecrets{token: "CLOUDFLARE_PLANNER_TOKEN" + suffix, passphrase: passphrase}
	case len(env.Parameters) > 0:
		return ciSecrets{token: "CLOUDFLARE_PREVIEW_TOKEN" + suffix, passphrase: passphrase}
	}
	return ciSecrets{token: "CLOUDFLARE_DEPLOYER_TOKEN" + suffix, passphrase: passphrase, environment: env.Environment}
}

// ciIdentities is the cloudflare target's CI seam.
type ciIdentities struct{}

var _ registry.CIIdentities = ciIdentities{}

// Identity names the secrets a CI job of env reads as role, which
// bootstrap's --github sets. It is nil for an environment with no account.
func (ciIdentities) Identity(env *ir.ResolvedEnvironment, role registry.CIRole) *registry.CIIdentity {
	if _, err := envValues(env); err != nil {
		return nil
	}
	s := ciSecretsOf(env, role)
	fields := map[string]string{"token": s.token, "passphrase": s.passphrase}
	if s.environment != "" {
		fields["environment"] = s.environment
	}
	return &registry.CIIdentity{Kind: CIIdentityKind, Fields: fields}
}
