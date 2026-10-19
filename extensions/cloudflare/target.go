package cloudflare

import (
	"fmt"
	"net/url"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The environment variables of the shell that runs the stack commands on a
// cloudflare environment (section 18.1), and of the generated CI's jobs.
const (
	// TokenEnv holds the API token every call to Cloudflare carries: the
	// engineer's, or the deployer's or the planner's in CI. The provider
	// reads it there too, so no node carries it.
	TokenEnv = "CLOUDFLARE_API_TOKEN"

	// PassphraseEnv holds the passphrase that encrypts the secrets in
	// Pulumi's state, which bootstrap generates and whoever deploys keeps.
	PassphraseEnv = "PULUMI_CONFIG_PASSPHRASE"
)

// targetValues is the schema of an environment's cloudflare values
// (section 18.1): the account every node names, 32 hexadecimal digits from
// the dashboard, and two values bootstrap records beside it through D47's
// BootstrapValue, which a person may correct: the account's workers.dev
// subdomain, under which an exposed Worker without a domain is reached,
// and the id of the account's Secrets Store, which every secret binding
// names.
const targetValues = `{
  "type": "object",
  "required": ["accountId"],
  "properties": {
    "accountId": {"type": "string", "pattern": "^[0-9a-f]{32}$"},
    "workersSubdomain": {"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"},
    "secretsStoreId": {"type": "string", "pattern": "^[0-9a-f]{32}$"}
  },
  "additionalProperties": false
}`

// accountValues are an environment's cloudflare values, which resolution has
// checked against targetValues.
type accountValues struct {
	accountID        string
	workersSubdomain string
	secretsStoreID   string
}

func targetValuesOf(m map[string]any) accountValues {
	var v accountValues
	v.accountID, _ = m["accountId"].(string)
	v.workersSubdomain, _ = m["workersSubdomain"].(string)
	v.secretsStoreID, _ = m["secretsStoreId"].(string)
	return v
}

// envValues reads the cloudflare values of a resolved environment.
func envValues(env *ir.ResolvedEnvironment) (accountValues, error) {
	v := targetValuesOf(env.Values)
	if v.accountID == "" {
		return v, fmt.Errorf("cloudflare: environment %s sets no accountId", env.Environment)
	}
	return v, nil
}

// The state's layout in R2 (section 18.3). One bucket per account holds
// Pulumi's state for every stack in it, under `.pulumi/`, and what the
// deploy keeps, under StatePrefix.
const (
	// StateBucket is the R2 bucket that holds the state: Pulumi's, the
	// deploy manifests, the passphrase's check, and the bundles' and sites'
	// files.
	StateBucket = "superschematic-state"

	// StatePrefix is where superschematic's own objects sit in the bucket.
	StatePrefix = "superschematic/"

	// ManifestPrefix holds each run's deploy manifest,
	// `<stack>/<run>.json`, and a history copy of each one written,
	// `<stack>/<run>/<time>.json`, since R2 keeps no versions.
	ManifestPrefix = StatePrefix + "manifests/"

	// BundlePrefix holds each bundle a deploy builds (D64),
	// `<stack>/<deployable>/<hex>/`, and SitePrefix each site's files,
	// `<stack>/<site>/<hex>/`, `<hex>` being the digest's. Each prefix
	// gets a marker object written last, and each file the mode it was
	// published with as metadata. No lifecycle rule applies: a rollback
	// takes an earlier digest's files.
	BundlePrefix = StatePrefix + "bundles/"
	SitePrefix   = StatePrefix + "sites/"

	// PassphraseCheck is the object that checks a passphrase: a random
	// salt and its HMAC under the passphrase, which bootstrap writes on its
	// first run.
	PassphraseCheck = StatePrefix + "passphrase-check"
)

// R2Endpoint is the S3 endpoint of an account's R2.
func R2Endpoint(account string) string {
	return "https://" + account + ".r2.cloudflarestorage.com"
}

// StateURL is the Pulumi backend URL of the state bucket at an S3
// endpoint: R2Endpoint's, or a stand-in's in tests. R2 takes region auto
// and path-style addressing; the credentials come from StateBackend.Env.
func StateURL(endpoint string) string {
	q := url.Values{}
	q.Set("endpoint", endpoint)
	q.Set("region", "auto")
	q.Set("use_path_style", "true")
	return "s3://" + StateBucket + "?" + q.Encode()
}

// kebab lowercases a name and joins its words with hyphens: Orders is
// orders, ShopAPIServer is shop-api-server, shop-api stays shop-api.
func kebab(name string) string {
	upper := func(i int) bool { return i >= 0 && i < len(name) && name[i] >= 'A' && name[i] <= 'Z' }
	lower := func(i int) bool {
		return i >= 0 && i < len(name) && (name[i] >= 'a' && name[i] <= 'z' || name[i] >= '0' && name[i] <= '9')
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case upper(i):
			if lower(i-1) || upper(i-1) && lower(i+1) {
				b.WriteByte('-')
			}
			b.WriteByte(c - 'A' + 'a')
		case c == '_':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// screaming is a name in upper case with its words joined by underscores,
// as a GitHub Actions secret's suffix takes an environment's name: Staging
// is STAGING, StagingEu is STAGING_EU.
func screaming(name string) string {
	return strings.ToUpper(strings.ReplaceAll(kebab(name), "-", "_"))
}
