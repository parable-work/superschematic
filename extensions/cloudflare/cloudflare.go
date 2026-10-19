// Package cloudflare is the Cloudflare extension of the stack model
// (docs/stack-model.md, sections 6.9 and 18, D64). It registers the DNS
// platform `cloudflare`, which writes an environment's domain records into
// a Cloudflare zone beside whatever target runs its compute, and the target
// `cloudflare`, which runs an environment on one Cloudflare account: its
// state in an R2 bucket, its secrets in the account's Secrets Store, a
// bootstrap that makes both and the CI's account tokens, and the generated
// CI's sign-in with those tokens. Its Workers, D1, R2, site and cron
// platforms come next, in this module.
//
// It registers through the public registry package alone, as any
// extension does (D10), in a Go module of its own (D1). Its platforms are
// pure functions to resource graph nodes typed by the Pulumi `cloudflare`
// provider, whose schemas the extension pins and registers (package
// schemas), so resolution checks every node offline. The provisioner that
// applies them is `pulumi`, another extension, given ProviderVersion for
// the `cloudflare` package. What it calls on Cloudflare directly, outside
// the provisioner, goes through Cloud, over the REST API and R2's S3 API.
package cloudflare

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare/schemas"
)

// The names the extension registers.
const (
	// Name is the extension's name.
	Name = "cloudflare"

	// DNS writes an environment's records into a Cloudflare zone. It is
	// the target's default DNS platform.
	DNS = "cloudflare"

	// Target is the target's name.
	Target = "cloudflare"

	// Provider is the provider each platform of the target runs on (D63).
	Provider = "cloudflare"

	// Provisioner is the provisioner the target names. The pulumi
	// extension registers it.
	Provisioner = "pulumi"

	// PolicyDomainOnCloudflare refuses an environment whose domain is not
	// on the cloudflare DNS platform: Cloudflare writes a custom domain's
	// record and certificate only in a zone of the account (section 18.11).
	PolicyDomainOnCloudflare = "domain-on-cloudflare"
)

// Package is the Pulumi package the extension's resource types belong to,
// the key of ProviderVersion in the pulumi provisioner's ProviderVersions.
const Package = "cloudflare"

// ProviderVersion is the pulumi-cloudflare release the resource types are
// pinned at (schemas/pulumi-cloudflare.json). The provisioner installs the
// provider at this version, so what resolution validated is what applies.
const ProviderVersion = "6.22.0"

// Extension is the Cloudflare extension. Its zero value is the one a
// distribution links; tests set its fields.
type Extension struct {
	// Cloud is what the target's bootstrap, state store and secret store
	// call on Cloudflare. Nil uses NewCloud's, over the REST and S3 APIs,
	// with the token in CLOUDFLARE_API_TOKEN; tests pass a fake.
	Cloud Cloud

	// GitHub sets the generated CI's secrets for bootstrap's --github.
	// Nil runs the gh CLI; tests pass a fake.
	GitHub GitHub

	// Getenv reads the shell's environment: TokenEnv and PassphraseEnv.
	// Nil is os.Getenv.
	Getenv func(string) string

	// Now is the clock a manifest's history copy is named by; nil is
	// time.Now. Rand is where a passphrase's and a check's random bytes come
	// from; nil is crypto/rand.
	Now  func() time.Time
	Rand io.Reader
}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the Cloudflare DNS platform and the cloudflare target,
// with the pinned schema of every type they and bootstrap emit, and the
// target's deploy seams: the state in R2, the Secrets Store, bootstrap and
// the generated CI's sign-in with an account token (D64).
func (e Extension) Register(r *registry.Registry) error {
	types, err := schemas.ResourceTypes(ProviderVersion)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	if err := r.RegisterDNSPlatform(registry.DNSPlatformSpec{
		Name:          DNS,
		Extension:     Name,
		Values:        json.RawMessage(dnsValues),
		Lower:         lowerRecords,
		ResourceTypes: types,
		Credentials:   credentials,
	}); err != nil {
		return err
	}
	platforms := map[ir.DeployableKind]string{}
	for _, spec := range e.platforms() {
		spec.Extension = Name
		spec.Provider = Provider
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
		platforms[spec.Kind] = spec.Name
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:          Target,
		Extension:     Name,
		Platforms:     platforms,
		Values:        json.RawMessage(targetValues),
		DNS:           DNS,
		Provisioner:   Provisioner,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyDomainOnCloudflare, Check: checkDomainOnCloudflare},
		},
		State:     stateStore{ext: e},
		Secrets:   secretStore{ext: e},
		Bootstrap: bootstrapper{ext: e},
		CI:        ciIdentities{},
	})
}

// platforms are the target's platforms, one per deployable kind it
// places; the target names each for its kind. Each gets the extension
// and the provider when it registers. None yet: Workers, Cron, D1, R2
// and sites come next (section 18.2).
func (e Extension) platforms() []registry.PlatformSpec {
	return nil
}

// getenv reads the shell's environment.
func (e Extension) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

// now is the extension's clock.
func (e Extension) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// random fills b with random bytes.
func (e Extension) random(b []byte) error {
	src := e.Rand
	if src == nil {
		src = rand.Reader
	}
	_, err := io.ReadFull(src, b)
	return err
}
