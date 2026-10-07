package gcp

import "github.com/parable-work/superschematic/registry"

// The resource types the target emits, as Pulumi `gcp` tokens. Each is
// pinned in schemas/.
const (
	TypeAccount             = "gcp:serviceaccount/account:Account"
	TypeProjectIAMMember    = "gcp:projects/iAMMember:IAMMember"
	TypeService             = "gcp:cloudrunv2/service:Service"
	TypeServiceIAMMember    = "gcp:cloudrunv2/serviceIamMember:ServiceIamMember"
	TypeSecret              = "gcp:secretmanager/secret:Secret"
	TypeSecretIAMMember     = "gcp:secretmanager/secretIamMember:SecretIamMember"
	TypeInstance            = "gcp:sql/databaseInstance:DatabaseInstance"
	TypeDatabase            = "gcp:sql/database:Database"
	TypeUser                = "gcp:sql/user:User"
	TypeNetwork             = "gcp:compute/network:Network"
	TypeSubnetwork          = "gcp:compute/subnetwork:Subnetwork"
	TypeRouter              = "gcp:compute/router:Router"
	TypeRouterNAT           = "gcp:compute/routerNat:RouterNat"
	TypeEndpointGroup       = "gcp:compute/regionNetworkEndpointGroup:RegionNetworkEndpointGroup"
	TypeBackendService      = "gcp:compute/backendService:BackendService"
	TypeURLMap              = "gcp:compute/uRLMap:URLMap"
	TypeHTTPSProxy          = "gcp:compute/targetHttpsProxy:TargetHttpsProxy"
	TypeForwardingRule      = "gcp:compute/globalForwardingRule:GlobalForwardingRule"
	TypeGlobalAddress       = "gcp:compute/globalAddress:GlobalAddress"
	TypeDNSAuthorization    = "gcp:certificatemanager/dnsAuthorization:DnsAuthorization"
	TypeCertificate         = "gcp:certificatemanager/certificate:Certificate"
	TypeCertificateMap      = "gcp:certificatemanager/certificateMap:CertificateMap"
	TypeCertificateMapEntry = "gcp:certificatemanager/certificateMapEntry:CertificateMapEntry"
	TypeRecordSet           = "gcp:dns/recordSet:RecordSet"

	// The types of the bootstrap graph (BootstrapEnvironment).
	TypeRepository               = "gcp:artifactregistry/repository:Repository"
	TypeRepositoryIAMMember      = "gcp:artifactregistry/repositoryIamMember:RepositoryIamMember"
	TypeServiceAccountIAMMember  = "gcp:serviceaccount/iAMMember:IAMMember"
	TypeBucketIAMMember          = "gcp:storage/bucketIAMMember:BucketIAMMember"
	TypeCryptoKeyIAMMember       = "gcp:kms/cryptoKeyIAMMember:CryptoKeyIAMMember"
	TypeWorkloadIdentityPool     = "gcp:iam/workloadIdentityPool:WorkloadIdentityPool"
	TypeWorkloadIdentityProvider = "gcp:iam/workloadIdentityPoolProvider:WorkloadIdentityPoolProvider"
)

// targetValues is the schema of an environment's gcp values: the project
// and region every resource lands in, and whether the environment is
// production, which the defaults and the policy rules read (section 7.5).
const targetValues = `{
  "type": "object",
  "required": ["project", "region"],
  "properties": {
    "project": {"type": "string", "pattern": "^[a-z][a-z0-9-]{4,28}[a-z0-9]$"},
    "region": {"type": "string", "pattern": "^[a-z]+-[a-z]+[0-9]+$"},
    "production": {"type": "boolean"}
  },
  "additionalProperties": false
}`

// cloudRunSettings is the schema of a server's settings on Cloud Run.
const cloudRunSettings = `{
  "type": "object",
  "properties": {
    "minInstances": {"type": "integer", "minimum": 0},
    "maxInstances": {"type": "integer", "minimum": 1},
    "concurrency": {"type": "integer", "minimum": 1, "maximum": 1000},
    "cpu": {"type": "string", "pattern": "^([0-9]+(\\.[0-9]+)?|[0-9]+m)$"},
    "memory": {"type": "string", "pattern": "^[0-9]+(Mi|Gi)$"}
  },
  "additionalProperties": false
}`

// cloudSQLSettings is the schema of a database's settings on Cloud SQL.
const cloudSQLSettings = `{
  "type": "object",
  "properties": {
    "tier": {"type": "string", "pattern": "^db-[a-z0-9-]+$"},
    "highAvailability": {"type": "boolean"},
    "version": {"enum": ["POSTGRES_15", "POSTGRES_16", "POSTGRES_17"]},
    "deletionProtection": {"type": "boolean"},
    "diskSize": {"type": "integer", "minimum": 10}
  },
  "additionalProperties": false
}`

// cloudDNSValues is the schema of an environment's Cloud DNS values: the
// managed zone that holds the domain and the project it lives in.
const cloudDNSValues = `{
  "type": "object",
  "properties": {
    "zone": {"type": "string", "pattern": "^[a-z]([-a-z0-9]*[a-z0-9])?$"},
    "project": {"type": "string", "pattern": "^[a-z][a-z0-9-]{4,28}[a-z0-9]$"}
  },
  "additionalProperties": false
}`

// values are an environment's gcp values.
type values struct {
	project    string
	region     string
	production bool
}

// valuesOf reads the values resolution checked against targetValues.
func valuesOf(env registry.StackEnvironment) values {
	project, _ := env.Values["project"].(string)
	region, _ := env.Values["region"].(string)
	production, _ := env.Values["production"].(bool)
	return values{project: project, region: region, production: production}
}

// The defaults section 7.5 and the platforms' settings fall back to.
const (
	// containerPort is the port every server listens on: Cloud Run's
	// default, which it also passes in PORT.
	containerPort = 8080

	// The paths of the entrypoint's health checks (section 8.1):
	// readinessPath answers 200 while every database the server connects
	// to answers, and 503 while it drains; livenessPath answers 200 while
	// the process runs.
	readinessPath = "/readyz"
	livenessPath  = "/healthz"

	// The startup probe asks readinessPath every startupPeriod seconds, each
	// time for at most startupTimeout, and fails the instance after
	// startupFailures misses: two minutes for its databases to answer.
	startupPeriod   = 5
	startupTimeout  = 4
	startupFailures = 24

	// The liveness probe asks livenessPath every livenessPeriod seconds, each
	// time for at most livenessTimeout, and restarts the instance after
	// livenessFailures misses in a row.
	livenessPeriod   = 15
	livenessTimeout  = 5
	livenessFailures = 3

	defaultCPU      = "1"
	defaultMemory   = "512Mi"
	defaultTier     = "db-custom-1-3840"
	defaultPostgres = "POSTGRES_16"

	// egressRange is the subnet Direct VPC egress takes its addresses from:
	// two per instance, so a /20 serves about two thousand.
	egressRange = "10.8.0.0/20"

	// recordTTL is the TTL of every record Cloud DNS writes.
	recordTTL = 300
)
