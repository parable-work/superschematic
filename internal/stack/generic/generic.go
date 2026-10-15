// Package generic is the core's generic connector (docs/stack-model.md,
// section 6.2, D63): the connector of an http or a site edge between two
// platforms of different providers that no connector names, such as a
// Cloud Run server calling a server on Cloudflare Workers, or a site in a
// Cloud Storage bucket whose browser calls a server on another cloud.
// Registry.Connector falls back to it; a connector that names both
// platforms always wins.
//
// It reaches the callee at its public address (PlatformSpec.PublicAddressOf,
// D55), so the callee must be exposed, which resolution checks
// (`cross-provider-unexposed`). An http edge authenticates with a key pair
// generated for the edge, a node of the Pulumi `tls` provider's
// PrivateKey with the Ed25519 algorithm: the caller signs a short-lived
// token with its private key, which its platform keeps in its secret store
// (ir.DerivedVariable.Secret), and the callee's ServiceAuthenticator
// verifies it against the public key in its callers field (section 9.2,
// D37). No long-lived cloud credential, such as a service account key,
// crosses to the other provider. A site edge carries no credential: the
// browser carries its end user's.
//
// The key comes out of the provider as PEM, the private key as PKCS#8 and
// the public key as SubjectPublicKeyInfo, which the HTTP runtimes' signed
// token source and verifier read beside a JWK, computing the key's kid as
// its RFC 7638 thumbprint. Each edge has one key slot: rotation, two slots
// replaced in turn by the `time` provider (section 9.2), is not built.
package generic

import (
	"errors"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/generic/schemas"
	ir "github.com/parable-work/superschematic/ir"
)

// The names the core registers.
const (
	// HTTPConnector serves an http edge between platforms of two
	// providers; SiteConnector a site edge.
	HTTPConnector = "generic.http"
	SiteConnector = "generic.site"
)

// The key node of an http edge.
const (
	// Package is the Pulumi package of the key node's type, whose plugin
	// version a distribution pins for the provisioner (ProviderVersion).
	Package = "tls"

	// ProviderVersion is the pulumi-tls release the key node's type is
	// pinned at (schemas/pulumi-tls.json). A distribution passes it to the
	// pulumi provisioner beside each target's, so what resolution
	// validated is what applies.
	ProviderVersion = "5.6.2"

	// TypeKeyPair is the key node's type: a private key the provider
	// generates and keeps in the provisioner's state, which no file and no
	// environment.json holds.
	TypeKeyPair = "tls:index/privateKey:PrivateKey"

	// KeyAlgorithm is the key node's algorithm.
	KeyAlgorithm = "ED25519"

	// PrivateKeyOutput is the key node's output that holds the private
	// key, as the PEM of its PKCS#8 encoding (RFC 8410). The provider marks
	// it secret.
	PrivateKeyOutput = "privateKeyPemPkcs8"

	// PublicKeyOutput is the key node's output that holds the public key,
	// as the PEM of its SubjectPublicKeyInfo (RFC 8410).
	PublicKeyOutput = "publicKeyPem"

	// SignedTokenLifetime is the longest, in seconds, a token the caller
	// signs may live: 5 minutes, as the HTTP runtimes' signed-token
	// sources sign them.
	SignedTokenLifetime = 300
)

// Register adds the generic connectors for http and site edges, with the
// pinned schema of the key node's type.
func Register(r *registry.Registry) error {
	types, err := schemas.ResourceTypes(ProviderVersion)
	if err != nil {
		return err
	}
	if err := r.RegisterConnector(registry.ConnectorSpec{
		Name:          HTTPConnector,
		Edge:          ir.EdgeHTTP,
		Generic:       true,
		ResourceTypes: types,
		Connect:       connectHTTP,
	}); err != nil {
		return err
	}
	return r.RegisterConnector(registry.ConnectorSpec{
		Name:    SiteConnector,
		Edge:    ir.EdgeSite,
		Generic: true,
		Connect: connectSite,
	})
}

// connectHTTP reaches the callee at its public address, with a token the
// caller signs with the edge's Ed25519 key (D37):
//
//   - the edge's key node, which the provisioner generates;
//   - the caller's service endpoint: the callee's public address, and a
//     signed-token credential whose iss and sub are the caller's deployable
//     and whose aud is the callee's, signed with the key node's private
//     key, a secret member its platform sets from its secret store;
//   - the callee's issuer: the caller as an issuer of its own, its key the
//     key node's public key, tokens that live at most
//     SignedTokenLifetime seconds, and the caller its one identity.
//
// Two edges from one caller to two APIs of one callee share the key node:
// the caller is one issuer to the callee. Resolution has checked that the
// callee is exposed (`cross-provider-unexposed`); Connect refuses one with
// no public address all the same.
func connectHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if !to.Exposed || to.PublicAddress == nil {
		return registry.Connected{}, errors.New("the generic connector reaches a server only at its public address, and the callee has none: expose it")
	}
	key := KeyPairID(from.Name, to.Name)
	serves := make([]string, len(from.Services))
	for i, ref := range from.Services {
		serves[i] = ref.Name
	}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:         key,
			Type:       TypeKeyPair,
			Properties: map[string]any{"algorithm": KeyAlgorithm},
			Phase:      ir.PhaseInfrastructure,
		}},
		Value: ir.ServiceEndpoint{
			URL: to.PublicAddress,
			Credential: &ir.ServiceCredential{
				Source:   ir.CredentialSignedToken,
				Audience: to.Name,
				Issuer:   from.Name,
				Key:      ir.Output{Resource: key, Name: PrivateKeyOutput},
			},
		},
		Callee: ir.ServiceAuthIssuer{
			Issuer:             from.Name,
			Audience:           to.Name,
			Algorithms:         []string{ir.AlgorithmEdDSA},
			Keys:               []ir.ServiceAuthKey{{PEM: ir.Output{Resource: key, Name: PublicKeyOutput}}},
			MaxLifetimeSeconds: SignedTokenLifetime,
			Callers:            []ir.ServiceAuthCaller{{Subject: from.Name, Deployable: from.Name, Serves: serves}},
		},
	}, nil
}

// connectSite derives the public address of the server whose API a site
// calls (D55), as every site connector does: the browser carries its end
// user's token, and the server answers the site's origin through its CORS
// field, so the edge needs no resource.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.SiteEndpoint{URL: ctx.To.PublicAddress}}, nil
}

// KeyPairID is the ID of the key node of the http edges from one
// deployable to a server of another provider: `<caller>.calls.<callee>.key`,
// the shape of the local target's key pair's, which no environment holds
// beside this one for the same two deployables, since one connector serves
// a pair of platforms.
func KeyPairID(caller, callee string) string { return caller + ".calls." + callee + ".key" }
