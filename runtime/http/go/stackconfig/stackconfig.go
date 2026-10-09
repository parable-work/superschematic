// Package stackconfig holds the config fields an API's edges derive in a
// stack (docs/stack-model.md, section 3.4): a database connection for the
// API's database, a service endpoint for each API it calls, a bucket
// connection for each bucket it lists (D54), and for an API with a service
// clause its callers field, what its server verifies a service credential
// against. The generated config loader and entrypoint read each from the
// environment variables a platform sets, one per member of the value the
// edges' connectors derived: the field's name, an underscore and the
// member's path in upper snake case (SHOP_DB_DATABASE_URL,
// SHOP_API_SERVICE_CREDENTIAL_SOURCE, SHOP_MEDIA_BUCKET_NAME,
// SHOP_API_CALLERS_ISSUERS_0_AUDIENCE). The members are those of
// ir.DatabaseConnection, ir.ServiceEndpoint, ir.BucketConnection and
// ir.ServiceAuth.
//
// The TypeScript HTTP runtime's loadDatabase, loadService, loadBucket and
// loadCallers read the same variables with the same refusals (D51). The vectors in
// runtime/http/testdata/stackconfig_parity.json, which this package's
// tests write with -update, hold both to one encoding.
package stackconfig

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Database is a database connection: exactly one of URL and CloudSQL.
type Database struct {
	// URL is a connection string.
	URL string

	// CloudSQL is a Cloud SQL connector configuration, which connects with
	// IAM database authentication.
	CloudSQL *CloudSQL
}

// CloudSQL is a Cloud SQL connector configuration.
type CloudSQL struct {
	// Instance is the instance connection name, project:region:instance.
	Instance string

	// Database is the database's name on the instance.
	Database string

	// User is the IAM database user.
	User string
}

// Service is a service endpoint: how the server reaches an API it calls.
type Service struct {
	// URL is the callee's base URL.
	URL string

	// Credential is the source of the service credential the client sends,
	// or nil for none.
	Credential *Credential
}

// The credential sources the HTTP runtimes ship (D37).
const (
	// SourceGoogleIDToken is a Google ID token for Audience from the
	// metadata server.
	SourceGoogleIDToken = "google-id-token"

	// SourceTokenFile is a token read from TokenFile.
	SourceTokenFile = "token-file"

	// SourceSignedToken is a token signed with Key.
	SourceSignedToken = "signed-token"
)

// ServiceAuthorizationHeader is the header a callee reads the service
// credential from, and the one a credential with no Headers travels in.
const ServiceAuthorizationHeader = "Service-Authorization"

// Credential is the source of a service credential and the headers that
// carry it. Only the members its Source reads are set.
type Credential struct {
	// Source is SourceGoogleIDToken, SourceTokenFile or SourceSignedToken.
	Source string

	// Audience is the credential's audience (google-id-token and
	// signed-token).
	Audience string

	// TokenFile is the file a token-file source reads.
	TokenFile string

	// Issuer is a signed token's iss and sub.
	Issuer string

	// Key is the private JWK a signed-token source signs with.
	Key string

	// Headers are the headers the credential travels in; empty means
	// ServiceAuthorizationHeader alone.
	Headers []string
}

// Bucket is a bucket connection: how the server reaches a bucket an API it
// serves lists in its buckets (D54). It holds no credential: on gcp the
// workload's own account reaches the bucket, and an emulator checks none.
type Bucket struct {
	// Name is the bucket's name with its provider.
	Name string

	// Endpoint is the base URL of an emulator that serves the provider's
	// API in its place, such as the local target's fake-gcs-server; empty
	// reaches the provider itself.
	Endpoint string
}

// HeaderNames returns the headers the credential travels in.
func (c *Credential) HeaderNames() []string {
	if len(c.Headers) == 0 {
		return []string{ServiceAuthorizationHeader}
	}
	return c.Headers
}

// LoadDatabase reads the database field named field from the environment:
// field_URL, or the three field_CLOUD_SQL_ variables.
func LoadDatabase(field string) (Database, error) {
	return loadDatabase(field, os.Getenv)
}

// LoadService reads the service field named field from the environment:
// field_URL, and the field_CREDENTIAL_ variables when field_CREDENTIAL_SOURCE
// is set.
func LoadService(field string) (Service, error) {
	return loadService(field, os.Getenv)
}

// LoadBucket reads the bucket field named field from the environment:
// field_NAME, and field_ENDPOINT when an emulator serves the bucket (D54).
func LoadBucket(field string) (Bucket, error) {
	return loadBucket(field, os.Getenv)
}

// endpointPattern is the shape of a bucket's endpoint: an http or https
// URL with a host.
var endpointPattern = regexp.MustCompile(`^https?://[^/?#]+`)

func loadBucket(field string, getenv func(string) string) (Bucket, error) {
	name := getenv(field + "_NAME")
	endpoint := getenv(field + "_ENDPOINT")
	var errs []error
	if name == "" {
		errs = append(errs, fmt.Errorf("required environment variable %s_NAME is not set", field))
	}
	if endpoint != "" && !endpointPattern.MatchString(endpoint) {
		errs = append(errs, fmt.Errorf("environment variable %s_ENDPOINT is %q; want an http or https URL", field, endpoint))
	}
	if len(errs) > 0 {
		return Bucket{}, errors.Join(errs...)
	}
	return Bucket{Name: name, Endpoint: strings.TrimRight(endpoint, "/")}, nil
}

func loadDatabase(field string, getenv func(string) string) (Database, error) {
	url := getenv(field + "_URL")
	cloud := []string{field + "_CLOUD_SQL_INSTANCE", field + "_CLOUD_SQL_DATABASE", field + "_CLOUD_SQL_USER"}
	values := make([]string, len(cloud))
	var set, unset []string
	for i, name := range cloud {
		values[i] = getenv(name)
		if values[i] == "" {
			unset = append(unset, name)
		} else {
			set = append(set, name)
		}
	}
	switch {
	case url != "" && len(set) > 0:
		return Database{}, fmt.Errorf("environment variables %s_URL and %s are both set; a database connection is a connection string or a Cloud SQL connector configuration, not both", field, strings.Join(set, ", "))
	case url != "":
		return Database{URL: url}, nil
	case len(set) == 0:
		return Database{}, fmt.Errorf("required environment variable %s_URL, or %s, is not set", field, strings.Join(cloud, ", "))
	case len(unset) > 0:
		return Database{}, fmt.Errorf("environment variables %s are set and %s is not; a Cloud SQL connection sets all three", strings.Join(set, ", "), strings.Join(unset, ", "))
	}
	return Database{CloudSQL: &CloudSQL{Instance: values[0], Database: values[1], User: values[2]}}, nil
}

func loadService(field string, getenv func(string) string) (Service, error) {
	url := getenv(field + "_URL")
	if url == "" {
		return Service{}, fmt.Errorf("required environment variable %s_URL is not set", field)
	}
	service := Service{URL: url}
	prefix := field + "_CREDENTIAL_"
	members := map[string]*string{}
	var credential Credential
	for name, dst := range map[string]*string{
		"AUDIENCE": &credential.Audience, "TOKEN_FILE": &credential.TokenFile,
		"ISSUER": &credential.Issuer, "KEY": &credential.Key,
	} {
		*dst = getenv(prefix + name)
		members[name] = dst
	}
	headers := getenv(prefix + "HEADERS")
	credential.Source = getenv(prefix + "SOURCE")
	if credential.Source == "" {
		for _, name := range []string{"AUDIENCE", "TOKEN_FILE", "ISSUER", "KEY"} {
			if *members[name] != "" {
				return Service{}, fmt.Errorf("environment variable %s%s is set and %sSOURCE is not", prefix, name, prefix)
			}
		}
		if headers != "" {
			return Service{}, fmt.Errorf("environment variable %sHEADERS is set and %sSOURCE is not", prefix, prefix)
		}
		return service, nil
	}
	var needs []string
	switch credential.Source {
	case SourceGoogleIDToken:
		needs = []string{"AUDIENCE"}
	case SourceTokenFile:
		needs = []string{"TOKEN_FILE"}
	case SourceSignedToken:
		needs = []string{"AUDIENCE", "ISSUER", "KEY"}
	default:
		return Service{}, fmt.Errorf("environment variable %sSOURCE is %q; want %s, %s or %s", prefix, credential.Source, SourceGoogleIDToken, SourceTokenFile, SourceSignedToken)
	}
	var errs []error
	for _, name := range []string{"AUDIENCE", "TOKEN_FILE", "ISSUER", "KEY"} {
		needed := false
		for _, need := range needs {
			needed = needed || need == name
		}
		switch value := *members[name]; {
		case needed && value == "":
			errs = append(errs, fmt.Errorf("required environment variable %s%s is not set: a %s credential reads it", prefix, name, credential.Source))
		case !needed && value != "":
			errs = append(errs, fmt.Errorf("environment variable %s%s is set, which a %s credential does not read", prefix, name, credential.Source))
		}
	}
	if headers != "" {
		hasServiceHeader := false
		for _, header := range strings.Split(headers, ",") {
			header = strings.TrimSpace(header)
			if header == "" {
				errs = append(errs, fmt.Errorf("environment variable %sHEADERS has an empty header name", prefix))
				continue
			}
			hasServiceHeader = hasServiceHeader || strings.EqualFold(header, ServiceAuthorizationHeader)
			credential.Headers = append(credential.Headers, header)
		}
		if !hasServiceHeader {
			errs = append(errs, fmt.Errorf("environment variable %sHEADERS lacks %s, the header the callee reads", prefix, ServiceAuthorizationHeader))
		}
	}
	if len(errs) > 0 {
		return Service{}, errors.Join(errs...)
	}
	service.Credential = &credential
	return service, nil
}
