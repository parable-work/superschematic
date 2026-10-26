package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
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
// reaches the database of an API it serves. Exactly one of URL, CloudSQL
// and D1 is set.
type DatabaseConnection struct {
	// URL is a connection string, such as
	// `postgres://user:password@host:5432/shop_db?sslmode=require`.
	URL any `json:"url,omitempty"`

	// CloudSQL is a Cloud SQL connector configuration, which connects with
	// IAM database authentication and so has no password (section 7.4).
	CloudSQL *CloudSQLConnection `json:"cloudSql,omitempty"`

	// D1 is a D1 database a Worker reaches through a binding (D64, section
	// 18.6). Only the TypeScript runtime's Workers entry reads it; the Go
	// and Rust readers refuse it.
	D1 *D1Connection `json:"d1,omitempty"`
}

// D1Connection is a D1 database bound to a Worker (D64). The binding is
// the grant: D1 has no roles, and a Worker bound to a database reads and
// writes all of it.
type D1Connection struct {
	// Binding is the name of the Worker's `d1` binding, which the platform
	// writes and the Workers entry takes from its env: the derived field's
	// name (`SHOP_DB_DATABASE`).
	Binding any `json:"binding"`

	// Database is the D1 database's uuid, an output of its node.
	Database any `json:"database"`
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

// BucketConnection is what a bucket edge's connector derives: how a server
// or a job reaches a bucket an API of it lists (D54). On gcp and on the
// local target it holds no credential: the workload's own account reaches
// the bucket, and the emulator checks none. On Workers it holds the
// binding that reaches the bucket and an R2 credential that signs URLs for
// it, since a Worker has no identity to sign with (D64, section 18.7).
type BucketConnection struct {
	// Name is the bucket's name with its provider: on gcp the GCS bucket's
	// name, which is unique across every project.
	Name any `json:"name"`

	// Endpoint is the base URL of an emulator that serves the provider's
	// API in its place, such as the local target's fake-gcs-server
	// (`http://127.0.0.1:24443`). Unset reaches the provider itself.
	Endpoint any `json:"endpoint,omitempty"`

	// Binding is the name of a Worker's `r2_bucket` binding to the bucket,
	// the derived field's name (`SHOP_MEDIA_BUCKET`), which the Workers
	// entry takes from its env (D64). Only the TypeScript runtime's Workers
	// entry reads it; the Go and Rust readers refuse it.
	Binding any `json:"binding,omitempty"`

	// R2 is the credential with which a Worker presigns S3 URLs for the
	// bucket's objects. It needs Binding.
	R2 *R2Credential `json:"r2,omitempty"`
}

// R2Credential is a per-edge account token scoped to one bucket's objects
// (D64, section 18.7): R2's S3 API takes the token's id as the access key
// and the hex SHA-256 of its value as the secret.
type R2Credential struct {
	// Account is the Cloudflare account's id, which names the S3 endpoint
	// `https://<account>.r2.cloudflarestorage.com`.
	Account any `json:"account"`

	// Jurisdiction is the bucket's jurisdiction, `eu` or `fedramp`, whose
	// buckets R2's S3 API serves at
	// `https://<account>.<jurisdiction>.r2.cloudflarestorage.com` (D64,
	// amended). Unset is the default jurisdiction, at Account's host.
	Jurisdiction any `json:"jurisdiction,omitempty"`

	// AccessKeyID is the token's id.
	AccessKeyID any `json:"accessKeyId"`

	// Token is the token's value. It is a secret member
	// (SecretDerivedMember): a connector derives it as a reference to an
	// output, and the platform sets its variable as a secret binding.
	Token any `json:"token"`
}

// ServiceBinding is a Workers service binding through which a Worker
// reaches the callee's Worker (D64, section 18.10): the binding admits the
// caller, and the credential's signed token identifies it.
type ServiceBinding struct {
	// Name is the binding's name in the caller's env: the derived field's
	// name (`SHOP_API_SERVICE`).
	Name any `json:"name"`

	// Worker is the callee's Worker, the script the binding names.
	Worker any `json:"worker"`
}

// ServiceEndpoint is what an http edge's connector derives: how a server
// reaches an API it calls.
type ServiceEndpoint struct {
	// URL is the callee's base URL.
	URL any `json:"url"`

	// Binding is the service binding a Worker calls the callee through,
	// with URL the address its requests carry (D64). Only the TypeScript
	// runtime's Workers entry reads it; the Go and Rust readers refuse it.
	Binding *ServiceBinding `json:"binding,omitempty"`

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

	// Key is the private key a signed-token source signs with: an Ed25519
	// private JWK's JSON, as the local target's key pair gives it, or an
	// Ed25519 private key's PKCS#8 PEM (RFC 8410), `-----BEGIN PRIVATE
	// KEY-----`, as the generic connector's key node gives it (D63). It is
	// a secret member (SecretDerivedMember): a connector derives it as a
	// reference to an output, and a platform sets its variable from the
	// caller's secret store (DerivedVariable.Secret).
	Key any `json:"key,omitempty"`

	// Headers are the headers the credential travels in, literal names
	// that include ServiceAuthorizationHeader. Empty means that header
	// alone. A Cloud Run caller adds `X-Serverless-Authorization` for a
	// callee whose invoker check is on.
	Headers []string `json:"headers,omitempty"`
}

// CheckDerivedValue checks a derived value against the contract of an
// edge of kind: a DatabaseConnection for sql, a ServiceEndpoint for http,
// a SiteEndpoint for site (D55), a BucketConnection for bucket (D54), a
// MemoryStoreConnection for memoryStore (D75).
// The members a Worker alone reads (D64) are among them: a database's d1,
// a service's binding, and a bucket's binding and r2.
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
	case EdgeSite:
		return checkSiteEndpoint(value)
	case EdgeBucket:
		return checkBucketConnection(value)
	case EdgeMemoryStore:
		return checkMemoryStoreConnection(value)
	}
	return fmt.Errorf("edge kind %q has no derived value", kind)
}

// checkMemoryStoreConnection checks a memory store connection (D75): a
// store, a URL and an authentication, a CA and a member when set. A
// literal URL's scheme is redis or rediss, a CA goes with rediss, and a
// literal authentication is iam or none, iam with rediss; a reference is
// the platform's to resolve.
func checkMemoryStoreConnection(v any) error {
	m, err := members(v, "a memory store connection", "store", "url", "ca", "auth", "member")
	if err != nil {
		return err
	}
	for _, name := range []string{"store", "url", "auth"} {
		if err := requiredString(m, "", name); err != nil {
			return err
		}
	}
	for _, name := range []string{"ca", "member"} {
		if value, ok := m[name]; ok {
			if err := stringMember(name, value); err != nil {
				return err
			}
		}
	}
	scheme := ""
	if url, ok := m["url"].(string); ok {
		switch {
		case strings.HasPrefix(url, "redis://"):
			scheme = "redis"
		case strings.HasPrefix(url, "rediss://"):
			scheme = "rediss"
		default:
			return fmt.Errorf("url is %q; a memory store's URL is redis:// or rediss://", url)
		}
	}
	if _, ok := m["ca"]; ok && scheme == "redis" {
		return errors.New("a memory store connection with a redis:// url sets no ca; a CA verifies a rediss:// server")
	}
	if auth, ok := m["auth"].(string); ok {
		switch auth {
		case MemoryStoreAuthNone:
		case MemoryStoreAuthIAM:
			if scheme == "redis" {
				return errors.New("a memory store connection with auth iam has a rediss:// url; an access token travels only over TLS")
			}
		default:
			return fmt.Errorf("auth is %q; want %s or %s", auth, MemoryStoreAuthIAM, MemoryStoreAuthNone)
		}
	}
	return nil
}

func checkBucketConnection(v any) error {
	m, err := members(v, "a bucket connection", "name", "endpoint", "binding", "r2")
	if err != nil {
		return err
	}
	if err := requiredString(m, "", "name"); err != nil {
		return err
	}
	endpoint, hasEndpoint := m["endpoint"]
	if hasEndpoint {
		if err := stringMember("endpoint", endpoint); err != nil {
			return err
		}
	}
	binding, hasBinding := m["binding"]
	if hasBinding {
		if hasEndpoint {
			return errors.New("a bucket connection sets endpoint or binding, not both: an emulator serves the bucket, or a Worker's binding reaches it")
		}
		if err := stringMember("binding", binding); err != nil {
			return err
		}
	}
	r2, hasR2 := m["r2"]
	if !hasR2 {
		return nil
	}
	if !hasBinding {
		return errors.New("a bucket connection with r2 sets binding: the credential signs URLs for the bucket the Worker's binding reaches")
	}
	m, err = members(r2, "r2", "account", "accessKeyId", "token", "jurisdiction")
	if err != nil {
		return err
	}
	for _, name := range []string{"account", "accessKeyId", "token"} {
		if err := requiredString(m, "r2.", name); err != nil {
			return err
		}
	}
	if jurisdiction, ok := m["jurisdiction"]; ok {
		return stringMember("r2.jurisdiction", jurisdiction)
	}
	return nil
}

// requiredStrings checks an object member at path whose members are each a
// string, all of them required.
func requiredStrings(v any, path string, names ...string) error {
	m, err := members(v, path, names...)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := requiredString(m, path+".", name); err != nil {
			return err
		}
	}
	return nil
}

func checkDatabaseConnection(v any) error {
	m, err := members(v, "a database connection", "url", "cloudSql", "d1")
	if err != nil {
		return err
	}
	var forms []string
	for _, form := range []string{"url", "cloudSql", "d1"} {
		if _, ok := m[form]; ok {
			forms = append(forms, form)
		}
	}
	switch len(forms) {
	case 0:
		return errors.New("a database connection sets url, cloudSql or d1")
	case 1:
	default:
		return fmt.Errorf("a database connection sets one of url, cloudSql and d1, not %s", strings.Join(forms, " and "))
	}
	switch forms[0] {
	case "url":
		return stringMember("url", m["url"])
	case "cloudSql":
		return requiredStrings(m["cloudSql"], "cloudSql", "instance", "database", "user")
	}
	return requiredStrings(m["d1"], "d1", "binding", "database")
}

func checkServiceEndpoint(v any) error {
	m, err := members(v, "a service endpoint", "url", "binding", "credential")
	if err != nil {
		return err
	}
	if err := requiredString(m, "", "url"); err != nil {
		return err
	}
	if binding, ok := m["binding"]; ok {
		if err := requiredStrings(binding, "binding", "name", "worker"); err != nil {
			return err
		}
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

	// Secret marks the variable of a secret member (SecretDerivedMember),
	// such as a signed-token credential's private key. A platform sets it
	// from its secret store, never as a plain value: gcp from a Secret
	// Manager secret whose version the value references, the local target
	// from its state directory, which its provisioner resolves the
	// reference from when it starts the process (D63).
	Secret bool
}

// secretDerivedMembers are the members of a derived value that hold a
// secret, by their paths as DerivedMembers names them.
var secretDerivedMembers = []string{"credential.key", "r2.token"}

// SecretDerivedMember reports whether the member of a derived value at
// path, dotted as DerivedMembers names it, holds a secret: a signed-token
// credential's private key, and an R2 credential's token (D64) (section
// 3.4). A connector
// derives a secret member as a reference to an output, never a literal,
// so environment.json never holds it, and a platform sets its variable
// from its secret store (DerivedVariable.Secret).
func SecretDerivedMember(path string) bool {
	return slices.Contains(secretDerivedMembers, path)
}

// DerivedVariables encodes a derived config field's value as environment
// variables, one per member (section 3.4). A member's variable is the
// field's name, an underscore, and the member's name in upper snake case;
// a nested member adds its own name the same way, and a list of strings
// is one variable that joins them with commas, a concatenation when one
// of them is a reference. A list of objects, or an
// empty list, is a variable that holds the list's length, and each
// object's members add the object's index to the list's name. A whole
// number is its decimal:
//
//	SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE  cloudSql.instance
//	SHOP_API_SERVICE_URL                 url
//	SHOP_API_SERVICE_CREDENTIAL_HEADERS  credential.headers
//	SHOP_API_CALLERS_ISSUERS             the length of issuers
//	SHOP_API_CALLERS_ISSUERS_0_AUDIENCE  issuers[0].audience
//
// A platform sets each variable on the server, and sets the variable of a
// secret member (SecretDerivedMember), which it marks Secret, from its
// secret store; the generated loaders read them back. The variables are
// sorted by name.
func DerivedVariables(field string, value any) ([]DerivedVariable, error) {
	v, err := jsonForm(value)
	if err != nil {
		return nil, err
	}
	var out []DerivedVariable
	// path is the member's path, dotted as DerivedMembers names it; a
	// list's objects keep their list's.
	var walk func(name, path string, v any) error
	walk = func(name, path string, v any) error {
		switch v := v.(type) {
		case map[string]any:
			if len(v) == 0 {
				return fmt.Errorf("%s: an empty object has no variable", name)
			}
			for _, key := range sortedMemberKeys(v) {
				inner := key
				if path != "" {
					inner = path + "." + key
				}
				if err := walk(name+"_"+DerivedVariableSegment(key), inner, v[key]); err != nil {
					return err
				}
			}
		case []any:
			if len(v) == 0 || isObject(v[0]) {
				out = append(out, DerivedVariable{Name: name, Value: strconv.Itoa(len(v))})
				for i, item := range v {
					if !isObject(item) {
						return fmt.Errorf("%s: a list member is %s; a list holds objects or strings, not both", name, describeMember(item))
					}
					if err := walk(name+"_"+strconv.Itoa(i), path, item); err != nil {
						return err
					}
				}
				return nil
			}
			parts := make([]string, len(v))
			referenced := false
			for i, item := range v {
				switch item := item.(type) {
				case Output, Parameter, Concat:
					referenced = true
				case string:
					if strings.Contains(item, ",") {
						return fmt.Errorf("%s: a list member is %s; a list holds strings without commas, or objects", name, describeMember(item))
					}
					parts[i] = item
				default:
					return fmt.Errorf("%s: a list member is %s; a list holds strings without commas, or objects", name, describeMember(item))
				}
			}
			if !referenced {
				out = append(out, DerivedVariable{Name: name, Value: strings.Join(parts, ",")})
				return nil
			}
			// A reference joins the others in a concatenation the platform
			// renders, as a site's origin does that a load balancer's
			// output names (D55).
			joined := Concat{}
			for i, item := range v {
				if i > 0 {
					joined = append(joined, ",")
				}
				if inner, ok := item.(Concat); ok {
					// A concatenation holds no other.
					joined = append(joined, inner...)
					continue
				}
				joined = append(joined, item)
			}
			out = append(out, DerivedVariable{Name: name, Value: joined})
		case float64:
			if v != math.Trunc(v) || math.Abs(v) > 1<<53 {
				return fmt.Errorf("%s: %s is not a whole number", name, describeMember(v))
			}
			out = append(out, DerivedVariable{Name: name, Value: strconv.FormatInt(int64(v), 10)})
		case string, Output, Parameter, Concat:
			out = append(out, DerivedVariable{Name: name, Value: v, Secret: SecretDerivedMember(path)})
		default:
			return fmt.Errorf("%s: %s is not a string or a reference", name, describeMember(v))
		}
		return nil
	}
	if err := walk(field, "", v); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isObject reports whether a JSON form is an object.
func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
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

// The core's rule for derived field names: the service's name in upper
// snake case, suffixed `_DATABASE` for a sql edge, `_SERVICE` for an http
// edge and `_BUCKET` for a bucket edge (`SHOP_DB_DATABASE`,
// `SHOP_API_SERVICE`, `SHOP_MEDIA_BUCKET`).
const (
	// ServicePlaceholder is what a derived field template replaces with
	// the service's name in upper snake case.
	ServicePlaceholder = "{SERVICE}"

	// DefaultDatabaseField is the core's template for a sql edge's field.
	DefaultDatabaseField = ServicePlaceholder + "_DATABASE"

	// DefaultServiceField is the core's template for an http edge's field.
	DefaultServiceField = ServicePlaceholder + "_SERVICE"

	// DefaultBucketField is the core's template for a bucket edge's field
	// (D54).
	DefaultBucketField = ServicePlaceholder + "_BUCKET"

	// DefaultMemoryStoreField is the core's template for a memoryStore
	// edge's field (D75).
	DefaultMemoryStoreField = ServicePlaceholder + "_MEMORY_STORE"
)

// DerivedFieldNames are the templates that name the config field each
// edge fills, the naming file's `[derived_fields]` (section 3.4). An empty
// template is the core's.
type DerivedFieldNames struct {
	// Database names a sql edge's field, after the DB service.
	Database string `json:"database,omitempty"`

	// Service names an http edge's field, after the called API service.
	Service string `json:"service,omitempty"`

	// Bucket names a bucket edge's field, after the Bucket service (D54).
	Bucket string `json:"bucket,omitempty"`

	// MemoryStore names a memoryStore edge's field, after the MemoryStore
	// service (D75).
	MemoryStore string `json:"memoryStore,omitempty"`
}

// Field returns the name of the config field an edge of kind to service
// fills. A site edge's is the called API service's name itself, the key
// of the API in the site's config (SiteConfigPath, D55), which no template
// names.
func (n DerivedFieldNames) Field(kind EdgeKind, service string) string {
	if kind == EdgeSite {
		return service
	}
	return strings.ReplaceAll(n.template(kind), ServicePlaceholder, EnvName(service))
}

func (n DerivedFieldNames) template(kind EdgeKind) string {
	if kind == EdgeSQL {
		if n.Database != "" {
			return n.Database
		}
		return DefaultDatabaseField
	}
	if kind == EdgeBucket {
		if n.Bucket != "" {
			return n.Bucket
		}
		return DefaultBucketField
	}
	if kind == EdgeMemoryStore {
		if n.MemoryStore != "" {
			return n.MemoryStore
		}
		return DefaultMemoryStoreField
	}
	if n.Service != "" {
		return n.Service
	}
	return DefaultServiceField
}

// Validate refuses a template that does not name the service once, or
// whose other characters would not make an environment variable's name:
// upper-case letters, digits and underscores, not starting with a digit.
func (n DerivedFieldNames) Validate() error {
	for _, t := range []struct{ key, template string }{{"database", n.Database}, {"service", n.Service}, {"bucket", n.Bucket}, {"memory_store", n.MemoryStore}} {
		if t.template == "" {
			continue
		}
		if strings.Count(t.template, ServicePlaceholder) != 1 {
			return fmt.Errorf("derived_fields.%s %q must contain %s once", t.key, t.template, ServicePlaceholder)
		}
		if !isEnvName(strings.ReplaceAll(t.template, ServicePlaceholder, "S")) {
			return fmt.Errorf("derived_fields.%s %q: outside %s, a template holds upper-case letters, digits and underscores, and does not start with a digit", t.key, t.template, ServicePlaceholder)
		}
	}
	return nil
}

func isEnvName(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return name != ""
}

// EnvName is a service's name in upper snake case: each letter upper case,
// each character that is not a letter or a digit an underscore
// (`shop-db` is `SHOP_DB`).
func EnvName(service string) string {
	var b strings.Builder
	for _, r := range service {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// DerivedFieldClaims reports whether a config field named name collides
// with the derived field: it is the field's name, or begins with it and an
// underscore, as each of the field's variables does (DerivedVariables).
// A member a later contract adds then cannot take a setting's name.
func DerivedFieldClaims(field, name string) bool {
	return name == field || strings.HasPrefix(name, field+"_")
}

// DerivedConfigField is a config field one of an API service's edges
// derives.
type DerivedConfigField struct {
	// Name is the field's name.
	Name string

	// Kind is the edge's kind: sql for the database, http for a call,
	// bucket for a bucket the API lists, memoryStore for a memory store it
	// lists.
	Kind EdgeKind

	// Service is the DB service, the called API service, the Bucket
	// service or the MemoryStore service.
	Service string

	// From is the config key the edge comes from: authDb, dependencies,
	// calls, buckets, memoryStores or rateLimitStore.
	From string
}

// Database returns the DB service an API service connects to: its
// `authDb`, or its one DB-kind dependency (section 3.3). It reports false
// for a schema with neither, or with several DB-kind dependencies and no
// `authDb`, and for a schema that is not an API.
func (s *Schema) Database() (service, from string, ok bool) {
	if s.Kind != SchemaKindAPI {
		return "", "", false
	}
	if s.AuthDB != "" {
		return s.AuthDB, "authDb", true
	}
	for _, dep := range s.Dependencies {
		if dep.Kind != SchemaKindDB {
			continue
		}
		if service != "" {
			return "", "", false
		}
		service = dep.Name
	}
	return service, "dependencies", service != ""
}

// DerivedConfigFields lists the config fields an API service's edges
// derive, named by names: its database's, then one per `calls` entry in
// order, then one per `buckets` entry in order (D54), then one per memory
// store in order (D75). A schema that is not an API has none.
func (s *Schema) DerivedConfigFields(names DerivedFieldNames) []DerivedConfigField {
	if s.Kind != SchemaKindAPI {
		return nil
	}
	var out []DerivedConfigField
	if db, from, ok := s.Database(); ok {
		out = append(out, DerivedConfigField{Name: names.Field(EdgeSQL, db), Kind: EdgeSQL, Service: db, From: from})
	}
	for _, call := range s.Calls {
		out = append(out, DerivedConfigField{Name: names.Field(EdgeHTTP, call.Name), Kind: EdgeHTTP, Service: call.Name, From: "calls"})
	}
	for _, bucket := range s.Buckets {
		out = append(out, DerivedConfigField{Name: names.Field(EdgeBucket, bucket.Name), Kind: EdgeBucket, Service: bucket.Name, From: "buckets"})
	}
	for _, store := range s.MemoryStores {
		from := "memoryStores"
		if s.RateLimitStore != nil && s.RateLimitStore.Name == store.Name {
			from = "rateLimitStore"
		}
		out = append(out, DerivedConfigField{Name: names.Field(EdgeMemoryStore, store.Name), Kind: EdgeMemoryStore, Service: store.Name, From: from})
	}
	return out
}

// DerivedMembers returns the paths of every member the value of an edge of
// kind can have, dotted for a nested member: what a derived field's
// variables are named after (DerivedVariableName).
func DerivedMembers(kind EdgeKind) []string {
	switch kind {
	case EdgeSQL:
		return []string{"url", "cloudSql.instance", "cloudSql.database", "cloudSql.user", "d1.binding", "d1.database"}
	case EdgeHTTP:
		return []string{"url", "binding.name", "binding.worker", "credential.source", "credential.audience", "credential.tokenFile", "credential.issuer", "credential.key", "credential.headers"}
	case EdgeSite:
		return []string{"url", "upstream", "binding.name", "binding.worker"}
	case EdgeBucket:
		return []string{"name", "endpoint", "binding", "r2.account", "r2.accessKeyId", "r2.token", "r2.jurisdiction"}
	case EdgeMemoryStore:
		return []string{"store", "url", "ca", "auth", "member"}
	}
	return nil
}

// DerivedVariableName returns the environment variable of the member at
// path, dotted, of the derived field named field: `SHOP_DB_DATABASE` and
// `cloudSql.instance` make `SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE`.
func DerivedVariableName(field, path string) string {
	name := field
	for _, segment := range strings.Split(path, ".") {
		name += "_" + DerivedVariableSegment(segment)
	}
	return name
}
