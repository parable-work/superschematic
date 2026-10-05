package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// The derived values: what a connector derives for an edge, and the
// generated config field the value fills (docs/stack-model.md, section
// 3.4). A connector returns one as its Connected.Value, and resolution
// checks it against the contract of the edge's kind (CheckDerivedValue).
// Every member but a credential's Source and Headers is a string or a
// reference (Output, Parameter, Concat) that resolves to one.

// DatabaseConnection is what a sql edge's connector derives: how a server
// reaches the database of an API it serves. Exactly one of URL and
// CloudSQL is set.
type DatabaseConnection struct {
	// URL is a connection string, such as
	// `postgres://user:password@host:5432/shop_db?sslmode=require`.
	URL any `json:"url,omitempty"`

	// CloudSQL is a Cloud SQL connector configuration, which connects with
	// IAM database authentication and so has no password (section 7.4).
	CloudSQL *CloudSQLConnection `json:"cloudSql,omitempty"`
}

// CloudSQLConnection is a Cloud SQL connector configuration.
type CloudSQLConnection struct {
	// Instance is the instance connection name, `project:region:instance`.
	Instance any `json:"instance"`

	// Database is the database's name on the instance.
	Database any `json:"database"`

	// User is the IAM database user the server connects as.
	User any `json:"user"`
}

// ServiceEndpoint is what an http edge's connector derives: how a server
// reaches an API it calls.
type ServiceEndpoint struct {
	// URL is the callee's base URL.
	URL any `json:"url"`

	// Credential is the source of the service credential the caller sends
	// (section 9.2). Without one the caller sends no service credential.
	Credential *ServiceCredential `json:"credential,omitempty"`
}

// ServiceCredentialSource names one of the service credential sources the
// HTTP runtimes ship (D37).
type ServiceCredentialSource string

const (
	// CredentialGoogleIDToken is a Google ID token for Audience, from the
	// metadata server: the Cloud Run caller's source.
	CredentialGoogleIDToken ServiceCredentialSource = "google-id-token"

	// CredentialTokenFile is a token read from TokenFile, which the
	// platform keeps current: the Kubernetes caller's projected service
	// account token.
	CredentialTokenFile ServiceCredentialSource = "token-file"

	// CredentialSignedToken is a token the caller signs with Key: the
	// generic connector's and the local target's source.
	CredentialSignedToken ServiceCredentialSource = "signed-token"
)

// ServiceAuthorizationHeader is the header the service authenticator
// reads the service credential from (section 9.2).
const ServiceAuthorizationHeader = "Service-Authorization"

// ServiceCredential is the source of a caller's service credential and
// the headers that carry it. A member its Source does not read is unset.
type ServiceCredential struct {
	// Source names the source. It is a literal, never a reference.
	Source ServiceCredentialSource `json:"source"`

	// Audience is the credential's audience: the callee's URL for a
	// Google ID token, the callee's deployable for a signed token.
	Audience any `json:"audience,omitempty"`

	// TokenFile is the file a token-file source reads.
	TokenFile any `json:"tokenFile,omitempty"`

	// Issuer is a signed token's `iss` and `sub`: the caller's deployable.
	Issuer any `json:"issuer,omitempty"`

	// Key is the private JWK a signed-token source signs with. It belongs
	// in the caller's secret store, which the variable encoding lets a
	// platform set it from (DerivedVariables).
	Key any `json:"key,omitempty"`

	// Headers are the headers the credential travels in, literal names
	// that include ServiceAuthorizationHeader. Empty means that header
	// alone. A Cloud Run caller adds `X-Serverless-Authorization` for a
	// callee whose invoker check is on.
	Headers []string `json:"headers,omitempty"`
}

// CheckDerivedValue checks a derived value against the contract of an
// edge of kind: a DatabaseConnection for sql, a ServiceEndpoint for http.
// v is the typed value or its JSON form; a member the contract does not
// have is refused. The error names the offending member.
func CheckDerivedValue(kind EdgeKind, v any) error {
	value, err := jsonForm(v)
	if err != nil {
		return err
	}
	switch kind {
	case EdgeSQL:
		return checkDatabaseConnection(value)
	case EdgeHTTP:
		return checkServiceEndpoint(value)
	}
	return fmt.Errorf("edge kind %q has no derived value", kind)
}

func checkDatabaseConnection(v any) error {
	m, err := members(v, "a database connection", "url", "cloudSql")
	if err != nil {
		return err
	}
	url, hasURL := m["url"]
	cloud, hasCloud := m["cloudSql"]
	switch {
	case hasURL && hasCloud:
		return errors.New("a database connection sets url or cloudSql, not both")
	case hasURL:
		return stringMember("url", url)
	case hasCloud:
		c, err := members(cloud, "cloudSql", "instance", "database", "user")
		if err != nil {
			return err
		}
		for _, name := range []string{"instance", "database", "user"} {
			if err := requiredString(c, "cloudSql.", name); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("a database connection sets url or cloudSql")
}

func checkServiceEndpoint(v any) error {
	m, err := members(v, "a service endpoint", "url", "credential")
	if err != nil {
		return err
	}
	if err := requiredString(m, "", "url"); err != nil {
		return err
	}
	cred, ok := m["credential"]
	if !ok {
		return nil
	}
	c, err := members(cred, "credential", "source", "audience", "tokenFile", "issuer", "key", "headers")
	if err != nil {
		return err
	}
	source, _ := c["source"].(string)
	var needs []string
	switch ServiceCredentialSource(source) {
	case CredentialGoogleIDToken:
		needs = []string{"audience"}
	case CredentialTokenFile:
		needs = []string{"tokenFile"}
	case CredentialSignedToken:
		needs = []string{"audience", "issuer", "key"}
	default:
		return fmt.Errorf("credential.source is %s; want %s, %s or %s", describeMember(c["source"]), CredentialGoogleIDToken, CredentialTokenFile, CredentialSignedToken)
	}
	for _, name := range []string{"audience", "tokenFile", "issuer", "key"} {
		if slices.Contains(needs, name) {
			if err := requiredString(c, "credential.", name); err != nil {
				return err
			}
		} else if _, set := c[name]; set {
			return fmt.Errorf("credential.%s is set, which a %s source does not read", name, source)
		}
	}
	if headers, ok := c["headers"]; ok {
		return checkHeaders(headers)
	}
	return nil
}

func checkHeaders(v any) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return errors.New("credential.headers must be a non-empty list of header names")
	}
	seen := map[string]bool{}
	for i, item := range list {
		name, ok := item.(string)
		if !ok || !isHeaderName(name) {
			return fmt.Errorf("credential.headers[%d] is %s, not a header name", i, describeMember(item))
		}
		canonical := strings.ToLower(name)
		if seen[canonical] {
			return fmt.Errorf("credential.headers lists %s twice", name)
		}
		seen[canonical] = true
	}
	if !seen[strings.ToLower(ServiceAuthorizationHeader)] {
		return fmt.Errorf("credential.headers lacks %s, the header the callee reads", ServiceAuthorizationHeader)
	}
	return nil
}

// isHeaderName reports whether name is an HTTP field name (RFC 9110's
// token), which never holds the comma DerivedVariables joins a list with.
func isHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// members returns v as an object, refusing a member not in allowed.
func members(v any, what string, allowed ...string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object, not %s", what, describeMember(v))
	}
	for _, key := range sortedMemberKeys(m) {
		if !slices.Contains(allowed, key) {
			return nil, fmt.Errorf("%s has no member %s; it has %s", what, key, strings.Join(allowed, ", "))
		}
	}
	return m, nil
}

func requiredString(m map[string]any, prefix, name string) error {
	v, ok := m[name]
	if !ok {
		return fmt.Errorf("%s%s is missing", prefix, name)
	}
	return stringMember(prefix+name, v)
}

// stringMember checks a member that holds a string: a non-empty literal
// or a reference.
func stringMember(path string, v any) error {
	switch v := v.(type) {
	case string:
		if v != "" {
			return nil
		}
	case Output, Parameter, Concat:
		return nil
	}
	return fmt.Errorf("%s is %s; want a non-empty string or a reference", path, describeMember(v))
}

func describeMember(v any) string {
	if v == nil {
		return "null"
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// jsonForm returns v as encoding/json decodes its JSON, with references
// turned back into Output, Parameter and Concat values: what
// environment.json holds and resolution reads.
func jsonForm(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return DecodeValue(raw)
}

// DerivedVariable is one environment variable of a derived config field.
type DerivedVariable struct {
	// Name is the variable's name.
	Name string

	// Value is a string or a reference.
	Value any
}

// DerivedVariables encodes a derived config field's value as environment
// variables, one per member (section 3.4). A member's variable is the
// field's name, an underscore, and the member's name in upper snake case;
// a nested member adds its own name the same way, and a list of strings
// is one variable that joins them with commas:
//
//	SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE  cloudSql.instance
//	SHOP_API_SERVICE_URL                 url
//	SHOP_API_SERVICE_CREDENTIAL_HEADERS  credential.headers
//
// A platform sets each variable on the server, and may set one from its
// secret store; the generated loaders read them back. The variables are
// sorted by name.
func DerivedVariables(field string, value any) ([]DerivedVariable, error) {
	v, err := jsonForm(value)
	if err != nil {
		return nil, err
	}
	var out []DerivedVariable
	var walk func(name string, v any) error
	walk = func(name string, v any) error {
		switch v := v.(type) {
		case map[string]any:
			if len(v) == 0 {
				return fmt.Errorf("%s: an empty object has no variable", name)
			}
			for _, key := range sortedMemberKeys(v) {
				if err := walk(name+"_"+DerivedVariableSegment(key), v[key]); err != nil {
					return err
				}
			}
		case []any:
			parts := make([]string, len(v))
			for i, item := range v {
				s, ok := item.(string)
				if !ok || strings.Contains(s, ",") {
					return fmt.Errorf("%s: a list member is %s; a list holds strings without commas", name, describeMember(item))
				}
				parts[i] = s
			}
			out = append(out, DerivedVariable{Name: name, Value: strings.Join(parts, ",")})
		case string, Output, Parameter, Concat:
			out = append(out, DerivedVariable{Name: name, Value: v})
		default:
			return fmt.Errorf("%s: %s is not a string or a reference", name, describeMember(v))
		}
		return nil
	}
	if err := walk(field, v); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DerivedVariableSegment turns a member's name into its part of a
// variable's name: upper snake case, so `cloudSql` is `CLOUD_SQL` and
// `tokenFile` is `TOKEN_FILE`.
func DerivedVariableSegment(member string) string {
	var b strings.Builder
	runes := []rune(member)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

func sortedMemberKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
