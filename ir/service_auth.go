package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The callee's side of service auth (D37; docs/stack-model.md, section
// 9.2): what the server of an API with a service clause verifies a
// caller's service credential against. Each http edge's connector returns
// how the callee verifies the edge's caller, a ServiceAuthIssuer listing
// that one caller (registry.Connected.Callee), and resolution merges the
// entries of the edges to an API into the value of the API's callers
// field, a ServiceAuth. The value maps onto the HTTP runtimes' serviceauth
// config: an issuer is a serviceauth issuer, with its callers as a list
// rather than a map keyed by subject, since a subject may be a reference,
// and each key as its JWK's JSON, since a key may be an output.

// CallersFieldSuffix is what the name of an API's callers field adds to
// the API's name in upper snake case.
const CallersFieldSuffix = "_CALLERS"

// CallersField returns the name of the callers field of the API service
// named service: its name in upper snake case and CallersFieldSuffix
// (`shop-api` is `SHOP_API_CALLERS`). It begins with no other derived
// field's name and an underscore, so a server that serves an API and calls
// it too holds both fields.
func CallersField(service string) string {
	return EnvName(service) + CallersFieldSuffix
}

// HasServiceCallers reports whether an operation of the schema has a
// service clause, its own or its set's (EffectiveServiceCallers). The
// API's server then verifies its service callers, against its callers
// field in a stack.
func (s *Schema) HasServiceCallers() bool {
	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if EffectiveServiceCallers(set, op) != nil {
				return true
			}
		}
	}
	return false
}

// The algorithms a service credential is signed with, as the
// verifier's config lists them (D37).
const (
	// AlgorithmRS256 signs a Google ID token and a Kubernetes service
	// account token.
	AlgorithmRS256 = "RS256"

	// AlgorithmES256 is accepted for a key the generic connector does not
	// generate.
	AlgorithmES256 = "ES256"

	// AlgorithmEdDSA signs the generic connector's and the local target's
	// key-pair tokens.
	AlgorithmEdDSA = "EdDSA"
)

// ServiceAuth is the value of an API's callers field: the issuers whose
// credentials the API's server accepts, each with the identities it
// vouches for that are callers. No issuers means no server calls the API
// in the environment, and every service credential is refused.
type ServiceAuth struct {
	// Issuers are sorted by issuer. No two share an issuer or an alias.
	Issuers []*ServiceAuthIssuer `json:"issuers"`
}

// ServiceAuthIssuer is one issuer of service credentials: where its keys
// are, what a token must carry, and the identities it vouches for that
// are callers. Issuer, Audience, JWKSURL, a key's JWK and a caller's
// Subject hold a string or a reference; the rest are literals.
type ServiceAuthIssuer struct {
	// Issuer is the `iss` the issuer writes.
	Issuer any `json:"issuer"`

	// IssuerAliases are other `iss` values of the same issuer, such as
	// Google's `accounts.google.com`.
	IssuerAliases []string `json:"issuerAliases,omitempty"`

	// Audience must be the token's `aud`, or one of its members.
	Audience any `json:"audience"`

	// Algorithms are the header `alg` values accepted: AlgorithmRS256,
	// AlgorithmES256 and AlgorithmEdDSA.
	Algorithms []string `json:"algorithms"`

	// JWKSURL is where the issuer publishes its keys. An issuer has
	// JWKSURL or Keys, not both.
	JWKSURL any `json:"jwksUrl,omitempty"`

	// Keys are the issuer's public keys.
	Keys []ServiceAuthKey `json:"keys,omitempty"`

	// SubjectClaim is the claim that names the caller; empty means `sub`.
	SubjectClaim string `json:"subjectClaim,omitempty"`

	// MaxLifetimeSeconds, when positive, refuses a token whose `exp` is
	// more than this after its `iat`.
	MaxLifetimeSeconds int64 `json:"maxLifetimeSeconds,omitempty"`

	// Callers are the identities the issuer vouches for that are callers,
	// sorted by deployable. A connector's entry lists the edge's caller
	// alone.
	Callers []ServiceAuthCaller `json:"callers"`
}

// ServiceAuthKey is a public key an issuer signs with.
type ServiceAuthKey struct {
	// JWK is the key's JWK (RFC 7517) as JSON, with a kid and without the
	// private member d: a string, or a reference to an output that holds
	// one, such as a key pair node's publicJwk.
	JWK any `json:"jwk"`
}

// ServiceAuthCaller is an identity that is a caller, and the deployable it
// is.
type ServiceAuthCaller struct {
	// Subject is the subject claim's value that names the caller.
	Subject any `json:"subject"`

	// Deployable is the calling deployable's name in the environment.
	Deployable string `json:"deployable"`

	// Serves are the APIs the deployable serves, sorted, which a route's
	// `from` is checked against.
	Serves []string `json:"serves"`
}

// CheckServiceAuth checks the value of a callers field: an object whose
// one member, issuers, lists issuers that each meet the contract of
// CheckServiceAuthIssuer, no two of which share an issuer or an alias.
// v is the typed value or its JSON form; the error names the member at
// fault.
func CheckServiceAuth(v any) error {
	value, err := jsonForm(v)
	if err != nil {
		return err
	}
	m, err := members(value, "a callers field", "issuers")
	if err != nil {
		return err
	}
	list, ok := m["issuers"].([]any)
	if !ok {
		return fmt.Errorf("issuers is %s; want a list of issuers", describeMember(m["issuers"]))
	}
	names := map[string]int{}
	for i, item := range list {
		if err := checkServiceAuthIssuer(item); err != nil {
			return fmt.Errorf("issuers[%d]: %w", i, err)
		}
		issuer := item.(map[string]any)
		ids := []string{describeMember(issuer["issuer"])}
		aliases, _ := issuer["issuerAliases"].([]any)
		for _, alias := range aliases {
			ids = append(ids, describeMember(alias))
		}
		for _, id := range ids {
			if prev, dup := names[id]; dup {
				return fmt.Errorf("issuers[%d] and issuers[%d] both name the issuer %s", prev, i, id)
			}
			names[id] = i
		}
	}
	return nil
}

// CheckServiceAuthIssuer checks an issuer of a callers field, as a
// connector derives one for an edge (registry.Connected.Callee): an
// issuer and an audience; one or more of the algorithms RS256, ES256 and
// EdDSA; a JWKS URL or one or more public keys, each a JWK's JSON with a
// kid; an optional subject claim, maximum lifetime and aliases; and one
// or more callers, each a subject, the deployable it is, and the APIs that
// deployable serves. A literal key must be a public JWK, and no two
// literal subjects may repeat.
func CheckServiceAuthIssuer(v any) error {
	value, err := jsonForm(v)
	if err != nil {
		return err
	}
	return checkServiceAuthIssuer(value)
}

func checkServiceAuthIssuer(v any) error {
	m, err := members(v, "an issuer", "issuer", "issuerAliases", "audience", "algorithms", "jwksUrl", "keys", "subjectClaim", "maxLifetimeSeconds", "callers")
	if err != nil {
		return err
	}
	if err := requiredString(m, "", "issuer"); err != nil {
		return err
	}
	if aliases, ok := m["issuerAliases"]; ok {
		if err := literalList("issuerAliases", aliases, nil); err != nil {
			return err
		}
		issuer := describeMember(m["issuer"])
		if slices.ContainsFunc(aliases.([]any), func(alias any) bool { return describeMember(alias) == issuer }) {
			return fmt.Errorf("issuerAliases repeats the issuer %s", issuer)
		}
	}
	if err := requiredString(m, "", "audience"); err != nil {
		return err
	}
	algorithms, ok := m["algorithms"]
	if !ok {
		return errors.New("algorithms is missing")
	}
	if err := literalList("algorithms", algorithms, []string{AlgorithmRS256, AlgorithmES256, AlgorithmEdDSA}); err != nil {
		return err
	}
	jwks, hasJWKS := m["jwksUrl"]
	keys, hasKeys := m["keys"]
	switch {
	case hasJWKS && hasKeys:
		return errors.New("an issuer sets jwksUrl or keys, not both")
	case hasJWKS:
		if err := stringMember("jwksUrl", jwks); err != nil {
			return err
		}
	case hasKeys:
		if err := checkKeys(keys); err != nil {
			return err
		}
	default:
		return errors.New("an issuer sets jwksUrl or keys")
	}
	if claim, ok := m["subjectClaim"]; ok {
		if s, isString := claim.(string); !isString || s == "" {
			return fmt.Errorf("subjectClaim is %s; want a claim's name", describeMember(claim))
		}
	}
	if lifetime, ok := m["maxLifetimeSeconds"]; ok {
		if n, isNumber := lifetime.(float64); !isNumber || n < 1 || n != float64(int64(n)) {
			return fmt.Errorf("maxLifetimeSeconds is %s; want a whole number of seconds above 0", describeMember(lifetime))
		}
	}
	return checkCallers(m["callers"])
}

// checkKeys checks an issuer's keys: a non-empty list of {jwk}, each a
// string or a reference, a literal one a public JWK with a kid.
func checkKeys(v any) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("keys is %s; want a non-empty list of keys", describeMember(v))
	}
	kids := map[string]bool{}
	for i, item := range list {
		key, err := members(item, fmt.Sprintf("keys[%d]", i), "jwk")
		if err != nil {
			return err
		}
		path := fmt.Sprintf("keys[%d].jwk", i)
		if err := requiredString(key, fmt.Sprintf("keys[%d].", i), "jwk"); err != nil {
			return err
		}
		literal, ok := key["jwk"].(string)
		if !ok {
			continue
		}
		var jwk map[string]any
		if err := json.Unmarshal([]byte(literal), &jwk); err != nil || jwk == nil {
			return fmt.Errorf("%s is not a JWK's JSON object", path)
		}
		if _, private := jwk["d"]; private {
			return fmt.Errorf("%s holds the private member d; a callee holds public keys only", path)
		}
		kty, _ := jwk["kty"].(string)
		kid, _ := jwk["kid"].(string)
		if kty == "" || kid == "" {
			return fmt.Errorf("%s needs a kty and a kid", path)
		}
		if kids[kid] {
			return fmt.Errorf("%s repeats the kid %s", path, kid)
		}
		kids[kid] = true
	}
	return nil
}

// checkCallers checks an issuer's callers: a non-empty list of {subject,
// deployable, serves}, no two with one literal subject.
func checkCallers(v any) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("callers is %s; want a non-empty list of callers", describeMember(v))
	}
	subjects := map[string]bool{}
	for i, item := range list {
		prefix := fmt.Sprintf("callers[%d].", i)
		c, err := members(item, fmt.Sprintf("callers[%d]", i), "subject", "deployable", "serves")
		if err != nil {
			return err
		}
		if err := requiredString(c, prefix, "subject"); err != nil {
			return err
		}
		if deployable, isString := c["deployable"].(string); !isString || deployable == "" {
			return fmt.Errorf("%sdeployable is %s; want the deployable's name", prefix, describeMember(c["deployable"]))
		}
		serves, ok := c["serves"]
		if !ok {
			return fmt.Errorf("%sserves is missing", prefix)
		}
		if err := literalList(prefix+"serves", serves, nil); err != nil {
			return err
		}
		if subject, isString := c["subject"].(string); isString {
			if subjects[subject] {
				return fmt.Errorf("%ssubject %s is another caller's", prefix, subject)
			}
			subjects[subject] = true
		}
	}
	return nil
}

// literalList checks a non-empty list of distinct literal strings without
// commas, which DerivedVariables joins with commas; with allowed, each
// must be one of them.
func literalList(path string, v any, allowed []string) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("%s is %s; want a non-empty list of strings", path, describeMember(v))
	}
	seen := map[string]bool{}
	for i, item := range list {
		s, ok := item.(string)
		if !ok || s == "" || strings.Contains(s, ",") {
			return fmt.Errorf("%s[%d] is %s; want a string without a comma", path, i, describeMember(item))
		}
		if allowed != nil && !slices.Contains(allowed, s) {
			return fmt.Errorf("%s[%d] is %s; want %s", path, i, s, strings.Join(allowed, ", "))
		}
		if seen[s] {
			return fmt.Errorf("%s lists %s twice", path, s)
		}
		seen[s] = true
	}
	return nil
}
