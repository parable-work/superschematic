package serviceauth

// Config is the service authenticator's configuration, the JSON each inbound
// edge's connector writes into the callee's config. The TypeScript and Rust
// runtimes read the same JSON.
type Config struct {
	// Issuers lists the credential issuers this server accepts. A token's
	// iss selects the entry whose Issuer or an alias equals it.
	Issuers []IssuerConfig `json:"issuers"`
	// LeewaySeconds is the clock skew allowed on exp, nbf and iat. Nil
	// means 60; an explicit 0 means none.
	LeewaySeconds *int64 `json:"leewaySeconds,omitempty"`
}

// IssuerConfig is one issuer of service credentials: where its keys are,
// what a token must carry, and which identities it vouches for are callers.
type IssuerConfig struct {
	// Issuer is the iss value the issuer writes.
	Issuer string `json:"issuer"`
	// IssuerAliases are other iss values for the same issuer, such as
	// Google's "accounts.google.com" beside "https://accounts.google.com".
	IssuerAliases []string `json:"issuerAliases,omitempty"`
	// Audience must be the token's aud, or one of its members.
	Audience string `json:"audience"`
	// Algorithms lists the accepted header algs: RS256, ES256 and EdDSA. A
	// token's alg Ed25519 (RFC 9864) is read as EdDSA.
	Algorithms []string `json:"algorithms"`
	// JWKSURL is where the issuer publishes its public keys. An entry has
	// JWKSURL or Keys, not both.
	JWKSURL string `json:"jwksUrl,omitempty"`
	// JWKSBearerTokenFile, with JWKSURL only, names a file whose trimmed
	// contents the key fetch sends as "Authorization: Bearer", as a
	// Kubernetes API server's /openid/v1/jwks wants.
	JWKSBearerTokenFile string `json:"jwksBearerTokenFile,omitempty"`
	// Keys are the issuer's public keys, held in the config.
	Keys []JWK `json:"keys,omitempty"`
	// SubjectClaim is the claim that names the caller. Empty means "sub".
	SubjectClaim string `json:"subjectClaim,omitempty"`
	// MaxLifetimeSeconds, when positive, requires iat and refuses a token
	// whose exp is more than this after its iat. Zero means no limit.
	MaxLifetimeSeconds int64 `json:"maxLifetimeSeconds,omitempty"`
	// Callers maps each subject claim value the server admits to the
	// deployable it is. An identity not listed is no caller (403).
	Callers map[string]CallerConfig `json:"callers"`
}

// CallerConfig is the deployable an identity is.
type CallerConfig struct {
	// Deployable is the calling deployable's name in the environment.
	Deployable string `json:"deployable"`
	// Serves lists the APIs the deployable serves, which a route's from is
	// checked against.
	Serves []string `json:"serves"`
}

// JWK is a JSON Web Key (RFC 7517) with the members the supported key types
// use. A config's keys and a fetched key set hold public keys only; D, the
// private member, is read by SignedToken.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	D   string `json:"d,omitempty"`
}

// JWKS is a JSON Web Key Set, the document a JWKSURL serves.
type JWKS struct {
	Keys []JWK `json:"keys"`
}
