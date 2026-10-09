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

// BucketConnection is what a bucket edge's connector derives: how a server
// or a job reaches a bucket an API of it lists (D54). It holds no
// credential: on gcp the workload's own account reaches the bucket, and the
// local target's emulator checks none.
type BucketConnection struct {
	// Name is the bucket's name with its provider: on gcp the GCS bucket's
	// name, which is unique across every project.
	Name any `json:"name"`

	// Endpoint is the base URL of an emulator that serves the provider's
	// API in its place, such as the local target's fake-gcs-server
	// (`http://127.0.0.1:24443`). Unset reaches the provider itself.
	Endpoint any `json:"endpoint,omitempty"`
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
// edge of kind: a DatabaseConnection for sql, a ServiceEndpoint for http,
// a BucketConnection for bucket.
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
	case EdgeBucket:
		return checkBucketConnection(value)
	}
	return fmt.Errorf("edge kind %q has no derived value", kind)
}

func checkBucketConnection(v any) error {
	m, err := members(v, "a bucket connection", "name", "endpoint")
	if err != nil {
		return err
	}
	if err := requiredString(m, "", "name"); err != nil {
		return err
	}
	if endpoint, ok := m["endpoint"]; ok {
		return stringMember("endpoint", endpoint)
	}
	return nil
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
// is one variable that joins them with commas. A list of objects, or an
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
			if len(v) == 0 || isObject(v[0]) {
				out = append(out, DerivedVariable{Name: name, Value: strconv.Itoa(len(v))})
				for i, item := range v {
					if !isObject(item) {
						return fmt.Errorf("%s: a list member is %s; a list holds objects or strings, not both", name, describeMember(item))
					}
					if err := walk(name+"_"+strconv.Itoa(i), item); err != nil {
						return err
					}
				}
				return nil
			}
			parts := make([]string, len(v))
			for i, item := range v {
				s, ok := item.(string)
				if !ok || strings.Contains(s, ",") {
					return fmt.Errorf("%s: a list member is %s; a list holds strings without commas, or objects", name, describeMember(item))
				}
				parts[i] = s
			}
			out = append(out, DerivedVariable{Name: name, Value: strings.Join(parts, ",")})
		case float64:
			if v != math.Trunc(v) || math.Abs(v) > 1<<53 {
				return fmt.Errorf("%s: %s is not a whole number", name, describeMember(v))
			}
			out = append(out, DerivedVariable{Name: name, Value: strconv.FormatInt(int64(v), 10)})
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
}

// Field returns the name of the config field an edge of kind to service
// fills.
func (n DerivedFieldNames) Field(kind EdgeKind, service string) string {
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
	if n.Service != "" {
		return n.Service
	}
	return DefaultServiceField
}

// Validate refuses a template that does not name the service once, or
// whose other characters would not make an environment variable's name:
// upper-case letters, digits and underscores, not starting with a digit.
func (n DerivedFieldNames) Validate() error {
	for _, t := range []struct{ key, template string }{{"database", n.Database}, {"service", n.Service}, {"bucket", n.Bucket}} {
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
	// bucket for a bucket the API lists.
	Kind EdgeKind

	// Service is the DB service, the called API service or the Bucket
	// service.
	Service string

	// From is the config key the edge comes from: authDb, dependencies,
	// calls or buckets.
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
// order, then one per `buckets` entry in order (D54). A schema that is not
// an API has none.
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
	return out
}

// DerivedMembers returns the paths of every member the value of an edge of
// kind can have, dotted for a nested member: what a derived field's
// variables are named after (DerivedVariableName).
func DerivedMembers(kind EdgeKind) []string {
	switch kind {
	case EdgeSQL:
		return []string{"url", "cloudSql.instance", "cloudSql.database", "cloudSql.user"}
	case EdgeHTTP:
		return []string{"url", "credential.source", "credential.audience", "credential.tokenFile", "credential.issuer", "credential.key", "credential.headers"}
	case EdgeBucket:
		return []string{"name", "endpoint"}
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
