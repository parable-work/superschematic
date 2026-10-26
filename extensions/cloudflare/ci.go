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
// A deploy job also reads the origin connection string of each database on
// Hyperdrive, which the migration runner migrates it with (D92): the field
// `env.<OriginEnv(db)>` names the secret the step's variable of that name
// reads.
func (ciIdentities) Identity(env *ir.ResolvedEnvironment, role registry.CIRole) *registry.CIIdentity {
	if _, err := envValues(env); err != nil {
		return nil
	}
	s := ciSecretsOf(env, role)
	fields := map[string]string{"token": s.token, "passphrase": s.passphrase}
	if s.environment != "" {
		fields["environment"] = s.environment
	}
	if role == registry.CIDeployer {
		for _, origin := range originSecretsOf(env) {
			fields["env."+origin.variable] = origin.secret
		}
	}
	return &registry.CIIdentity{Kind: CIIdentityKind, Fields: fields}
}

// originSecret is the GitHub secret a deploy job reads a database's origin
// connection string from, and the variable it sets (D92).
type originSecret struct {
	db, variable, secret string
}

// originSecretsOf are the origin secrets env's deploy job reads: one per
// database on Hyperdrive, OriginEnv(<db>)_<STACK>_<ENV>, in the deploy
// job's GitHub environment, whose reviewers approve it, beside the
// deployer's token. A parameterized environment has none: Hyperdrive
// refuses its databases.
func originSecretsOf(env *ir.ResolvedEnvironment) []originSecret {
	if len(env.Parameters) > 0 {
		return nil
	}
	suffix := "_" + screaming(env.Stack) + "_" + screaming(env.Environment)
	var out []originSecret
	for _, d := range env.Deployables {
		if d.Platform != HyperdrivePlatform || len(d.Services) != 1 {
			continue
		}
		db := d.Services[0].Name
		out = append(out, originSecret{db: db, variable: OriginEnv(db), secret: OriginEnv(db) + suffix})
	}
	return out
}
