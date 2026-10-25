package local

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	// Loopback is the address every local server and the Postgres
	// container are reached at.
	Loopback = "127.0.0.1"

	// DefaultPostgresImage is the image the Postgres container runs when
	// the environment's values name none: Postgres 16, the version CI
	// tests against and the gcp target runs.
	DefaultPostgresImage = "postgres:16-alpine"

	// PostgresUser is the role every connection uses. The container trusts
	// every connection, and publishes its port on loopback only.
	PostgresUser = "postgres"

	// postgresPort is the port Postgres listens on inside its container.
	postgresPort = 5432

	// PostgresContainer is the ID of the environment's Postgres container
	// node, which every database deployable's lowering returns, so they
	// share it.
	PostgresContainer = "postgres.container"

	// PortVariable is the environment variable a server listens on, as the
	// generated entrypoint reads it (docs/stack-model.md, section 8.1).
	PortVariable = "PORT"

	// KeyAlgorithm is the algorithm of an edge's key pair; TokenAlgorithm
	// is the alg its tokens carry and the callee accepts (D37).
	KeyAlgorithm   = "Ed25519"
	TokenAlgorithm = "EdDSA"

	// SignedTokenLifetime is how long, in seconds, a token signed with an
	// edge's key lives, exp minus iat, and the most the callee accepts
	// (D37).
	SignedTokenLifetime = 300

	// ReadinessPath is the path the generated entrypoint answers once it
	// is ready; HealthPath once it runs.
	ReadinessPath = "/readyz"
	HealthPath    = "/healthz"

	// ReadinessStarted is the readiness of a process with no port, a
	// worker's: ready once it starts (D53).
	ReadinessStarted = "started"

	// ReadinessListening is the readiness of an app's process (D78): ready
	// once its port accepts a connection. The app is the project's own
	// program, which answers no readiness path of the entrypoint's.
	ReadinessListening = "listening"

	// WorkerConcurrencyVariable is the environment variable a worker reads
	// its concurrency from, which the local platform sets.
	WorkerConcurrencyVariable = ir.WorkerConcurrencyVariable

	// ServerModuleDir is the directory, under the output root, of each
	// server's entrypoint module: `server/<stack>/<server>`.
	ServerModuleDir = "server"

	// DefaultStorageImage is the image the storage emulator's container
	// runs when the environment's values name none: fake-gcs-server, which
	// serves GCS's JSON API, and the object paths a signed URL names, on
	// plain HTTP (D54).
	DefaultStorageImage = "fsouza/fake-gcs-server:1.56.1"

	// storagePort is the port the storage emulator listens on inside its
	// container.
	storagePort = 4443

	// StorageContainer is the ID of the environment's storage emulator's
	// container node, which every bucket deployable's lowering returns, so
	// they share it.
	StorageContainer = "storage.container"

	// StorageReadinessPath is the emulator's path the provisioner probes
	// until it answers 200: its list of buckets, under which it creates
	// each bucket.
	StorageReadinessPath = "/storage/v1/b"

	// StorageProject is the project the provisioner creates each bucket
	// in on the emulator, which keeps one.
	StorageProject = "local"

	// DefaultMemoryStoreImage is the image the memory store's container
	// runs when the environment's values name none: Valkey 8.0, the
	// engine version the gcp platform pins (VALKEY_8_0), with no
	// persistence and no TLS (D75).
	DefaultMemoryStoreImage = "valkey/valkey:8.0.2-alpine"

	// memoryStorePort is the port Valkey listens on inside its container.
	memoryStorePort = 6379

	// MemoryStoreContainer is the ID of the environment's memory store
	// container node, which every memoryStore deployable's lowering
	// returns, so every store of the environment shares one server: each
	// store's names start with the store's own name (D75).
	MemoryStoreContainer = "memorystore.container"

	// MemoryStoreDatabase is the database every store's URL names. A test
	// that needs a real server uses another, so it never touches the keys
	// of a running stack dev (D75).
	MemoryStoreDatabase = 0
)

// A port the environment does not set falls in a range by a hash of the
// stack, the environment and, for a server, the deployable, so it stays
// the same from run to run and differs between environments. The
// checkout's discriminator shifts every such port of an environment by
// one offset within its range, so two checkouts of a repository run the
// environment side by side, while two of its ports meet in one checkout
// only when they meet in every checkout, and `local-distinct-ports` says
// the same of the environment wherever it resolves (D30, amended). The
// servers' range, the Postgres container's, the storage emulator's and
// the memory store's do not overlap, and all sit below the ephemeral
// ranges of Linux (32768 and up) and macOS (49152 and up).
const (
	serverPortBase      = 20000
	storagePortBase     = 23000
	memoryStorePortBase = 26000
	postgresPortBase    = 30000
	portSpan            = 2768
)

// hashPort returns base plus a hash of parts shifted by the checkout's
// offset, below base+portSpan.
func hashPort(base int, checkout string, parts ...string) int {
	h := fnv.New32a()
	for i, part := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(part))
	}
	return base + (int(h.Sum32()%portSpan)+portOffset(checkout))%portSpan
}

// portOffset is how far a checkout shifts each port hashPort derives: its
// discriminator read as a hexadecimal number, modulo portSpan, and zero for
// no checkout.
func portOffset(checkout string) int {
	n, err := strconv.ParseUint(checkout, 16, 32)
	if err != nil {
		return 0
	}
	return int(n % portSpan)
}

// checkoutBytes is how many bytes of its hash a checkout's discriminator
// holds: six hexadecimal digits.
const checkoutBytes = 3

// Checkout returns the discriminator of the checkout a local environment
// runs from: the first six hexadecimal digits of a SHA-256 of the absolute
// path of its state directory (StateDir), or empty when schemasRoot is
// empty. Two checkouts of a repository differ, and one keeps its
// discriminator from run to run, so it keeps its containers (D30,
// amended). schemasRoot is the canonical root resolution reads
// (stack.CanonicalSchemasRoot).
func Checkout(schemasRoot, stack, environment string) string {
	if schemasRoot == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(StateDir(schemasRoot, stack, environment))))
	return hex.EncodeToString(sum[:checkoutBytes])
}

// checkoutOf is the discriminator of the checkout env resolves in.
func checkoutOf(env registry.StackEnvironment) string {
	return Checkout(env.SchemasRoot, env.Stack, env.Name)
}

// ServerPort returns the port a server listens on: its `port` setting, or
// one derived from the stack, the environment, the server's name and the
// checkout.
func ServerPort(env registry.StackEnvironment, d ir.ResolvedDeployable) int {
	if port, ok := intValue(d.Settings["port"]); ok {
		return port
	}
	return hashPort(serverPortBase, checkoutOf(env), env.Stack, env.Name, d.Name)
}

// PostgresPort returns the host port the environment's Postgres container
// publishes: the `postgresPort` value, or one derived from the stack, the
// environment and the checkout.
func PostgresPort(env registry.StackEnvironment) int {
	if port, ok := intValue(env.Values["postgresPort"]); ok {
		return port
	}
	return hashPort(postgresPortBase, checkoutOf(env), env.Stack, env.Name)
}

// PostgresImage returns the image the environment's Postgres container
// runs: the `postgresImage` value, or DefaultPostgresImage.
func PostgresImage(env registry.StackEnvironment) string {
	if image, ok := env.Values["postgresImage"].(string); ok && image != "" {
		return image
	}
	return DefaultPostgresImage
}

// maxContainerName is the longest name the local target gives a
// container: a DNS label's 63 bytes, so the name also serves as the
// container's host name on a Docker network.
const maxContainerName = 63

// containerName returns the name of an environment's container that plays
// role: `superschematic-<stack>-<environment>-<checkout>-<role>`, lower
// case, or `superschematic-<stack>-<environment>-<role>` for no checkout,
// the name before checkouts were told apart. A name with a checkout that
// would be longer than maxContainerName loses the end of its stack and
// environment, which the checkout, a hash of a directory named after both,
// still tells apart.
func containerName(stack, environment, checkout, role string) string {
	head := strings.ToLower("superschematic-" + stack + "-" + environment)
	if checkout == "" {
		return head + "-" + role
	}
	tail := "-" + checkout + "-" + role
	if len(head)+len(tail) > maxContainerName {
		head = strings.TrimRight(head[:maxContainerName-len(tail)], "-_.")
	}
	return head + tail
}

// ContainerName returns the name of the environment's Postgres container
// in a checkout (Checkout):
// `superschematic-<stack>-<environment>-<checkout>-postgres`, lower case.
func ContainerName(stack, environment, checkout string) string {
	return containerName(stack, environment, checkout, "postgres")
}

// The labels of each container of an environment, which say whose it is:
// the stack's name, the environment's, and the checkout's discriminator
// (Checkout), which a container of no checkout lacks.
const (
	LabelStack       = "superschematic.stack"
	LabelEnvironment = "superschematic.environment"
	LabelCheckout    = "superschematic.checkout"
)

// containerLabels are the labels of env's containers.
func containerLabels(env registry.StackEnvironment) map[string]any {
	labels := map[string]any{
		LabelStack:       env.Stack,
		LabelEnvironment: env.Name,
	}
	if checkout := checkoutOf(env); checkout != "" {
		labels[LabelCheckout] = checkout
	}
	return labels
}

// StoragePort returns the host port the environment's storage emulator
// publishes: the `storagePort` value, or one derived from the stack, the
// environment and the checkout (D54).
func StoragePort(env registry.StackEnvironment) int {
	if port, ok := intValue(env.Values["storagePort"]); ok {
		return port
	}
	return hashPort(storagePortBase, checkoutOf(env), env.Stack, env.Name)
}

// StorageImage returns the image the environment's storage emulator runs:
// the `storageImage` value, or DefaultStorageImage.
func StorageImage(env registry.StackEnvironment) string {
	if image, ok := env.Values["storageImage"].(string); ok && image != "" {
		return image
	}
	return DefaultStorageImage
}

// StorageContainerName returns the name of the environment's storage
// emulator's container in a checkout (Checkout):
// `superschematic-<stack>-<environment>-<checkout>-storage`, lower case.
func StorageContainerName(stack, environment, checkout string) string {
	return containerName(stack, environment, checkout, "storage")
}

// StorageURL returns the base URL of a storage emulator published on port:
// what a bucket edge derives as its endpoint.
func StorageURL(port int) string {
	return fmt.Sprintf("http://%s:%d", Loopback, port)
}

// MemoryStorePort returns the host port the environment's memory store
// container publishes: the `memoryStorePort` value, or one derived from the
// stack, the environment and the checkout (D75).
func MemoryStorePort(env registry.StackEnvironment) int {
	if port, ok := intValue(env.Values["memoryStorePort"]); ok {
		return port
	}
	return hashPort(memoryStorePortBase, checkoutOf(env), env.Stack, env.Name)
}

// MemoryStoreImage returns the image the environment's memory store
// container runs: the `memoryStoreImage` value, or DefaultMemoryStoreImage.
func MemoryStoreImage(env registry.StackEnvironment) string {
	if image, ok := env.Values["memoryStoreImage"].(string); ok && image != "" {
		return image
	}
	return DefaultMemoryStoreImage
}

// MemoryStoreContainerName returns the name of the environment's memory
// store container in a checkout (Checkout):
// `superschematic-<stack>-<environment>-<checkout>-memorystore`, lower
// case.
func MemoryStoreContainerName(stack, environment, checkout string) string {
	return containerName(stack, environment, checkout, "memorystore")
}

// MemoryStoreURL returns the URL of a memory store container published on
// port: what a memoryStore edge derives, `redis://127.0.0.1:<port>/0`.
func MemoryStoreURL(port int) string {
	return fmt.Sprintf("redis://%s:%d/%d", Loopback, port, MemoryStoreDatabase)
}

// bucketNamePattern is the shape of a bucket's name on GCS and its
// emulator: 3 to 63 lower-case letters, digits, hyphens and underscores,
// starting and ending with a letter or a digit.
var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$`)

// BucketName returns the name of a Bucket service's bucket on the
// environment's emulator: the service's name in lower case. Each
// environment runs an emulator of its own, so no other stack's bucket
// takes the name.
func BucketName(service string) string { return strings.ToLower(service) }

// DatabaseName returns the name of a hosted DB schema's database: the
// service's name in lower snake case (`shop-db` is `shop_db`).
func DatabaseName(service string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(service) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "_" + name
	}
	return name
}

// DatabaseURL returns the connection string of a database on the
// environment's Postgres container. The container trusts every
// connection, so it holds no password.
func DatabaseURL(port int, database string) string {
	return fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", PostgresUser, Loopback, port, database)
}

// ServerURL returns the base URL of a server listening on port.
func ServerURL(port int) string {
	return fmt.Sprintf("http://%s:%d", Loopback, port)
}

// ModulePath returns a server's entrypoint module, relative to the output
// root: `server/<stack>/<server>` (docs/stack-model.md, section 8.1).
func ModulePath(stack, server string) string {
	return path.Join(ServerModuleDir, stack, server)
}

// intValue reads a whole number from a setting or a value: an int, or the
// float64 JSON decodes it as.
func intValue(v any) (int, bool) {
	switch v := v.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v == float64(int(v)) {
			return int(v), true
		}
	}
	return 0, false
}

// kebab lowercases a deployable's name and joins its words with hyphens:
// Orders is orders, shop-api stays shop-api.
func kebab(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && name[i-1] != '-' && name[i-1] != '_' {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
		case r == '_':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// processName names a server's process after its deployable.
func processName(ctx registry.PlatformContext) any { return kebab(ctx.Deployable.Name) }

// processAddress is the server's loopback URL.
func processAddress(ctx registry.PlatformContext) any {
	return ServerURL(ServerPort(ctx.Environment, ctx.Deployable))
}

// databaseDeployableName names a database deployable after itself; its
// databases are named after the schemas it hosts.
func databaseDeployableName(ctx registry.PlatformContext) any { return kebab(ctx.Deployable.Name) }

// databaseAddress is the container's published address.
func databaseAddress(ctx registry.PlatformContext) any {
	return Loopback + ":" + strconv.Itoa(PostgresPort(ctx.Environment))
}

// postgresContainer is the environment's Postgres container node, named
// for the checkout. Every database deployable returns the same node, so
// they share it. It is ready once pg_isready answers inside it.
func postgresContainer(env registry.StackEnvironment) *ir.Resource {
	return &ir.Resource{
		ID:   PostgresContainer,
		Type: TypeContainer,
		Properties: map[string]any{
			"name":  ContainerName(env.Stack, env.Name, checkoutOf(env)),
			"image": PostgresImage(env),
			"ports": []any{map[string]any{
				"host":          Loopback,
				"hostPort":      PostgresPort(env),
				"containerPort": postgresPort,
			}},
			"env":    []any{map[string]any{"name": "POSTGRES_HOST_AUTH_METHOD", "value": "trust"}},
			"labels": containerLabels(env),
			"readiness": map[string]any{"exec": []any{
				"pg_isready", "--host", Loopback, "--port", strconv.Itoa(postgresPort), "--username", PostgresUser, "--quiet",
			}},
		},
		Phase: ir.PhaseInfrastructure,
	}
}

// storageContainer is the environment's storage emulator's container node
// (D54), named for the checkout: fake-gcs-server on plain HTTP, keeping
// its objects in the container's filesystem, so a stopped container keeps
// them for the next run. The emulator writes the URLs it hands out with
// the host's view of its address. Every bucket deployable returns the same
// node, so they share it. It is ready once it answers its list of buckets.
func storageContainer(env registry.StackEnvironment) *ir.Resource {
	port := StoragePort(env)
	host := Loopback + ":" + strconv.Itoa(port)
	return &ir.Resource{
		ID:   StorageContainer,
		Type: TypeContainer,
		Properties: map[string]any{
			"name":  StorageContainerName(env.Stack, env.Name, checkoutOf(env)),
			"image": StorageImage(env),
			"ports": []any{map[string]any{
				"host":          Loopback,
				"hostPort":      port,
				"containerPort": storagePort,
			}},
			"args": []any{
				"-scheme", "http", "-host", "0.0.0.0", "-port", strconv.Itoa(storagePort),
				"-backend", "filesystem", "-filesystem-root", "/storage",
				"-public-host", host, "-external-url", StorageURL(port),
			},
			"labels":    containerLabels(env),
			"readiness": map[string]any{"http": StorageReadinessPath},
		},
		Phase: ir.PhaseInfrastructure,
	}
}

// memoryStoreContainer is the environment's memory store container node
// (D75), named for the checkout: Valkey with no persistence, so a stopped
// container keeps nothing, and the store holds no fact to keep. Every
// memoryStore deployable returns the same node, so they share it. It is
// ready once valkey-cli's PING answers inside it.
func memoryStoreContainer(env registry.StackEnvironment) *ir.Resource {
	return &ir.Resource{
		ID:   MemoryStoreContainer,
		Type: TypeContainer,
		Properties: map[string]any{
			"name":  MemoryStoreContainerName(env.Stack, env.Name, checkoutOf(env)),
			"image": MemoryStoreImage(env),
			"ports": []any{map[string]any{
				"host":          Loopback,
				"hostPort":      MemoryStorePort(env),
				"containerPort": memoryStorePort,
			}},
			"args":   []any{"valkey-server", "--save", "", "--appendonly", "no"},
			"labels": containerLabels(env),
			"readiness": map[string]any{"exec": []any{
				"valkey-cli", "-p", strconv.Itoa(memoryStorePort), "ping",
			}},
		},
		Phase: ir.PhaseInfrastructure,
	}
}

// memoryStoreDeployableName names a memoryStore deployable after its
// service, whose name starts every name it holds.
func memoryStoreDeployableName(ctx registry.PlatformContext) any {
	if len(ctx.Deployable.Services) == 1 {
		return ctx.Deployable.Services[0].Name
	}
	return kebab(ctx.Deployable.Name)
}

// memoryStoreAddress is the memory store container's URL.
func memoryStoreAddress(ctx registry.PlatformContext) any {
	return MemoryStoreURL(MemoryStorePort(ctx.Environment))
}

// lowerMemoryStore lowers a memoryStore deployable to the environment's
// memory store container. The store needs nothing of its own on it: a
// store's names start with its own name, and every key expires.
func lowerMemoryStore(ctx registry.PlatformContext) (registry.Lowered, error) {
	return registry.Lowered{Resources: []*ir.Resource{memoryStoreContainer(ctx.Environment)}}, nil
}

// connectMemoryStore derives how a server, a job, a worker or a deriver
// reaches the store: its service's name, the container's redis URL, and no
// authentication, since the container checks none and publishes its port
// on loopback only (D75). The local target refuses a parameterized
// environment, so a connection names no member. The edge needs no
// resource.
func connectMemoryStore(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.MemoryStoreConnection{
		Store: ctx.Edge.Service.Name,
		URL:   ctx.To.Address,
		Auth:  ir.MemoryStoreAuthNone,
	}}, nil
}

// bucketDeployableName names a bucket deployable after its bucket on the
// emulator (BucketName).
func bucketDeployableName(ctx registry.PlatformContext) any {
	if len(ctx.Deployable.Services) == 1 {
		return BucketName(ctx.Deployable.Services[0].Name)
	}
	return BucketName(ctx.Deployable.Name)
}

// bucketAddress is the storage emulator's base URL.
func bucketAddress(ctx registry.PlatformContext) any {
	return StorageURL(StoragePort(ctx.Environment))
}

// bucketID is the ID of a bucket deployable's bucket node.
func bucketID(deployable string) string { return deployable + ".bucket" }

// lowerBucket lowers a bucket deployable to the environment's storage
// emulator and a bucket on it, which the provisioner creates through the
// emulator's JSON API (D54).
func lowerBucket(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	name, _ := d.ResourceName.(string)
	if !bucketNamePattern.MatchString(name) {
		return registry.Lowered{}, fmt.Errorf("the bucket of %s would be named %q; a bucket's name is 3 to 63 lower-case letters, digits, hyphens and underscores, starting and ending with a letter or a digit", d.Name, name)
	}
	service := d.Name
	if len(d.Services) == 1 {
		service = d.Services[0].Name
	}
	return registry.Lowered{Resources: []*ir.Resource{
		storageContainer(ctx.Environment),
		{
			ID:   bucketID(d.Name),
			Type: TypeBucket,
			Properties: map[string]any{
				"name":      name,
				"service":   service,
				"container": ir.Output{Resource: StorageContainer, Name: "name"},
				"endpoint":  StorageURL(StoragePort(ctx.Environment)),
			},
		},
	}}, nil
}

// connectBucket derives the bucket's name on the environment's storage
// emulator and the emulator's endpoint, which the Go, TypeScript and Rust
// runtimes' GCS implementations reach with no credential (D54). The edge
// needs no resource: the emulator checks no caller.
func connectBucket(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.BucketConnection{Name: ctx.To.ResourceName, Endpoint: ctx.To.Address}}, nil
}

// lowerDatabase lowers a database deployable to the environment's Postgres
// container and a database on it per hosted DB schema.
func lowerDatabase(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	out := registry.Lowered{Resources: []*ir.Resource{postgresContainer(ctx.Environment)}}
	port := PostgresPort(ctx.Environment)
	for _, svc := range d.Services {
		name := DatabaseName(svc.Name)
		if len(name) > 63 {
			return registry.Lowered{}, fmt.Errorf("the database of %s would be named %s, longer than the 63 bytes Postgres allows", svc.Name, name)
		}
		out.Resources = append(out.Resources, &ir.Resource{
			ID:   databaseID(d.Name, svc.Name),
			Type: TypeDatabase,
			Properties: map[string]any{
				"name":      name,
				"service":   svc.Name,
				"container": ir.Output{Resource: PostgresContainer, Name: "name"},
				"url":       DatabaseURL(port, name),
			},
		})
	}
	return out, nil
}

// databaseID is the ID of the node of a hosted DB schema's database.
func databaseID(deployable, service string) string {
	return deployable + ".database." + service
}

// SQLiteDir is the directory, under the environment's state directory,
// that holds the SQLite file of each sqlite-first database (D64).
const SQLiteDir = "databases"

// SQLiteFile returns the file of a hosted DB schema's SQLite database,
// relative to the environment's state directory:
// `databases/<database>.sqlite`, the database named as on Postgres
// (DatabaseName).
func SQLiteFile(service string) string {
	return path.Join(SQLiteDir, DatabaseName(service)+".sqlite")
}

// SQLiteURL returns the URL of the SQLite file at file, an absolute path:
// `sqlite:<file>`, which the migration runner and the TypeScript runtime's
// sqliteFile both read as the path.
func SQLiteURL(file string) string {
	return "sqlite:" + filepath.ToSlash(file)
}

// lowerSQLite lowers a database deployable whose DB services list sqlite
// first to a SQLite file per hosted DB schema under the environment's
// state directory (D64; docs/stack-model.md, section 18.13). No container
// runs it: the provisioner creates the file, the migration runner migrates
// it from the SQLite plan, and the server opens it with openSqlite. The
// node's url output is the file's sqlite: URL, which only the provisioner
// knows, since the state directory is the machine's.
func lowerSQLite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	var out registry.Lowered
	for _, svc := range d.Services {
		out.Resources = append(out.Resources, &ir.Resource{
			ID:   databaseID(d.Name, svc.Name),
			Type: TypeSQLiteDatabase,
			Properties: map[string]any{
				"name":    DatabaseName(svc.Name),
				"service": svc.Name,
				"file":    SQLiteFile(svc.Name),
			},
		})
	}
	return out, nil
}

// connectSQLite derives the sqlite: URL of the edge's database's file, a
// reference to its node's url output, which the provisioner resolves when
// it starts the server. The edge needs no resource: a file checks no
// caller.
func connectSQLite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{
		URL: ir.Output{Resource: databaseID(ctx.To.Name, ctx.Edge.Service.Name), Name: "url"},
	}}, nil
}

// processID is the ID of a server's process node.
func processID(deployable string) string { return deployable + ".process" }

// lowerProcess lowers a server to its process: the entrypoint module the
// build wrote, the port, and an environment variable per binding. A
// literal is its value, a secret names the secret the provisioner reads
// from the environment's secrets file, a parameter references the
// parameter, and a derived field is one variable per member of its value,
// as ir.DerivedVariables encodes it (section 3.4). The provisioner sets
// PORT, so no binding may take it.
func lowerProcess(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env, err := processEnv(d, func(name string) error {
		if name == PortVariable {
			return fmt.Errorf("config field %s is the variable the local platform sets to the %s's port; set the port with the %s's port setting instead", PortVariable, d.Kind, d.Kind)
		}
		return nil
	})
	if err != nil {
		return registry.Lowered{}, err
	}
	props := map[string]any{
		"name":      d.ResourceName,
		"module":    ModulePath(ctx.Environment.Stack, d.Name),
		"language":  strings.ToLower(d.Language),
		"port":      ServerPort(ctx.Environment, d),
		"readiness": ReadinessPath,
	}
	if len(env) > 0 {
		props["env"] = env
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:         processID(d.Name),
		Type:       TypeProcess,
		Properties: props,
	}}}, nil
}

// processEnv is an environment variable per binding of a server or a job:
// a literal is its value, a secret names the secret the provisioner reads
// from the environment's secrets file, a parameter references the
// parameter, and a derived field is one variable per member of its value,
// as ir.DerivedVariables encodes it (section 3.4). A secret member, the
// signed-token key of an http edge, already comes from the target's secret
// store: its value references the key pair node's privateJwk, which the
// provisioner reads from the environment's state directory when it starts
// the process, so no file under the output root holds it (D63). claim
// refuses a name the platform sets itself.
func processEnv(d ir.ResolvedDeployable, claim func(string) error) ([]any, error) {
	var env []any
	for _, b := range d.Bindings {
		if b.Source == ir.BindingDerived {
			vars, err := ir.DerivedVariables(b.Field, b.Value)
			if err != nil {
				return nil, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			for _, v := range vars {
				if err := claim(v.Name); err != nil {
					return nil, err
				}
				env = append(env, map[string]any{"name": v.Name, "value": v.Value})
			}
			continue
		}
		if err := claim(b.Field); err != nil {
			return nil, err
		}
		entry := map[string]any{"name": b.Field}
		switch b.Source {
		case ir.BindingLiteral:
			entry["value"] = b.Value
		case ir.BindingParameter:
			entry["value"] = ir.Parameter(b.Parameter)
		case ir.BindingSecret:
			entry["secret"] = b.Secret
		default:
			return nil, fmt.Errorf("binding %s has source %q", b.Field, b.Source)
		}
		env = append(env, entry)
	}
	return env, nil
}

// lowerWorker lowers a worker to its process (D53): the entrypoint module
// the build wrote at `server/<stack>/<worker>`, its environment as a
// server's is, with WorkerConcurrencyVariable set to the concurrency the
// environment gives it, no port, and a readiness of started. stack dev
// runs one process for a worker whatever its instances, and none for one
// the environment turns off.
func lowerWorker(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Worker == nil {
		return registry.Lowered{}, fmt.Errorf("worker %s has no run", d.Name)
	}
	if d.Worker.Instances == 0 {
		return registry.Lowered{}, nil
	}
	env, err := processEnv(d, func(name string) error {
		if name == WorkerConcurrencyVariable {
			return fmt.Errorf("config field %s is the variable the local platform sets to the worker's concurrency; set it with the worker's concurrency setting instead", WorkerConcurrencyVariable)
		}
		return nil
	})
	if err != nil {
		return registry.Lowered{}, err
	}
	env = append(env, map[string]any{"name": WorkerConcurrencyVariable, "value": strconv.Itoa(d.Worker.Concurrency)})
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:   processID(d.Name),
		Type: TypeProcess,
		Properties: map[string]any{
			"name":      d.ResourceName,
			"module":    ModulePath(ctx.Environment.Stack, d.Name),
			"language":  strings.ToLower(d.Language),
			"kind":      "worker",
			"readiness": ReadinessStarted,
			"env":       env,
		},
	}}}, nil
}

// lowerRunner lowers an engine's runner to its process (D58): the
// entrypoint module the build wrote at `server/<stack>/<runner>`, its
// environment its sql edge's connection string, no port, and a readiness
// of started. It runs the engine's runner while it holds the runner's lock
// on the environment's Postgres; stack dev runs one, and none when the
// environment turns it off.
func lowerRunner(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Runner == nil {
		return registry.Lowered{}, fmt.Errorf("runner %s has no engine database", d.Name)
	}
	if d.Runner.Instances == 0 {
		return registry.Lowered{}, nil
	}
	env, err := processEnv(d, func(string) error { return nil })
	if err != nil {
		return registry.Lowered{}, err
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:   processID(d.Name),
		Type: TypeProcess,
		Properties: map[string]any{
			"name":      d.ResourceName,
			"module":    ModulePath(ctx.Environment.Stack, d.Name),
			"language":  strings.ToLower(d.Language),
			"kind":      "runner",
			"readiness": ReadinessStarted,
			"env":       env,
		},
	}}}, nil
}

// lowerDeriver lowers a deriver to its process (D74): the entrypoint
// module the build wrote at `server/<stack>/<deriver>`, its environment as
// a server's is, with WorkerConcurrencyVariable set to the concurrency the
// environment gives it, no port, and a readiness of started. stack dev
// runs one process for it, and none when the environment turns it off.
func lowerDeriver(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Deriver == nil {
		return registry.Lowered{}, fmt.Errorf("deriver %s has no run", d.Name)
	}
	if d.Deriver.Instances == 0 {
		return registry.Lowered{}, nil
	}
	env, err := processEnv(d, func(name string) error {
		if name == WorkerConcurrencyVariable {
			return fmt.Errorf("config field %s is the variable the local platform sets to the deriver's concurrency; set it with the deriver's concurrency setting instead", WorkerConcurrencyVariable)
		}
		return nil
	})
	if err != nil {
		return registry.Lowered{}, err
	}
	env = append(env, map[string]any{"name": WorkerConcurrencyVariable, "value": strconv.Itoa(d.Deriver.Concurrency)})
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:   processID(d.Name),
		Type: TypeProcess,
		Properties: map[string]any{
			"name":      d.ResourceName,
			"module":    ModulePath(ctx.Environment.Stack, d.Name),
			"language":  strings.ToLower(d.Language),
			"kind":      "deriver",
			"readiness": ReadinessStarted,
			"env":       env,
		},
	}}}, nil
}

// appAddress is an app's loopback URL, or none for an app the
// environment turns off, which runs no process (D78).
func appAddress(ctx registry.PlatformContext) any {
	if app := ctx.Deployable.App; app == nil || !app.Enabled {
		return nil
	}
	return processAddress(ctx)
}

// lowerApp lowers an app to its process (D78), as a Rust server is lowered
// (lowerProcess): the entrypoint crate the build wrote at
// `server/<stack>/<app>`, its environment as its API's server's, and the
// port its settings give it or one derived as a server's is. It is ready
// once it listens there (ReadinessListening). stack dev runs none for an
// app the environment turns off.
func lowerApp(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.App == nil {
		return registry.Lowered{}, fmt.Errorf("app %s has no run", d.Name)
	}
	if !d.App.Enabled {
		return registry.Lowered{}, nil
	}
	out, err := lowerProcess(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	process := out.Resources[0].Properties
	process["kind"] = "app"
	process["readiness"] = ReadinessListening
	return out, nil
}

// jobID is the ID of a job's node.
func jobID(deployable string) string { return deployable + ".job" }

// lowerJob lowers a job to its node (D52): the entrypoint module the build
// wrote at `server/<stack>/<job>`, its environment as a server's is but
// with no port, the method of its API's Jobs interface it runs, and the
// schedule, time zone, timeout and retries resolution decided. The
// provisioner builds it with the servers and runs it on its schedule while
// the environment runs; `stack run` runs it once.
func lowerJob(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Job == nil {
		return registry.Lowered{}, fmt.Errorf("job %s has no run", d.Name)
	}
	env, err := processEnv(d, func(string) error { return nil })
	if err != nil {
		return registry.Lowered{}, err
	}
	props := map[string]any{
		"name":           d.ResourceName,
		"module":         ModulePath(ctx.Environment.Stack, d.Name),
		"language":       strings.ToLower(d.Language),
		"api":            d.Job.API,
		"job":            d.Job.Name,
		"timeZone":       d.Job.TimeZone,
		"timeoutSeconds": d.Job.TimeoutSeconds,
		"retries":        d.Job.Retries,
	}
	if d.Job.Schedule != "" {
		props["schedule"] = d.Job.Schedule
	}
	if len(env) > 0 {
		props["env"] = env
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:         jobID(d.Name),
		Type:       TypeJob,
		Properties: props,
	}}}, nil
}

// siteAddress is a site's loopback URL, its origin: on the port its
// setting names, or one derived as a server's is (D55).
func siteAddress(ctx registry.PlatformContext) any {
	return ServerURL(ServerPort(ctx.Environment, ctx.Deployable))
}

// siteID is the ID of a site's node.
func siteID(deployable string) string { return deployable + ".site" }

// SiteDevPort returns the port a site's dev server listens on (D82): its
// config's port, or one derived as a server's is, from the stack, the
// environment, the site's name and the checkout.
func SiteDevPort(env registry.StackEnvironment, d ir.ResolvedDeployable) int {
	if d.Site != nil && d.Site.Dev != nil && d.Site.Dev.Port != 0 {
		return d.Site.Dev.Port
	}
	return hashPort(serverPortBase, checkoutOf(env), env.Stack, env.Name, d.Name, "dev")
}

// lowerSite lowers a site to its node (D55): its package, relative to the
// output root, the script that builds it and the directory the build
// writes, its single-page fallback, the port its file server listens on,
// and its config, each API it calls with the loopback URL its site edge
// derives. The provisioner builds it once and serves it until the
// environment stops. A site with a dev server (D82) holds its command and
// the port it listens on too: the provisioner runs it in place of the
// build, and serves the site's port from a proxy in front of it.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	apis := map[string]any{}
	for _, b := range d.Bindings {
		value, _ := b.Value.(map[string]any)
		url, ok := value["url"].(string)
		if b.Source != ir.BindingDerived || !ok {
			return registry.Lowered{}, fmt.Errorf("site %s: binding %s holds no URL of the local target's", d.Name, b.Field)
		}
		apis[b.Field] = map[string]any{"url": url}
	}
	props := map[string]any{
		"name":   d.ResourceName,
		"dir":    d.Site.Dir,
		"build":  d.Site.Build,
		"output": d.Site.Output,
		"port":   ServerPort(ctx.Environment, d),
		"config": map[string]any{"apis": apis},
	}
	if d.Site.Fallback != "" {
		props["fallback"] = d.Site.Fallback
	}
	if dev := d.Site.Dev; dev != nil {
		command := dev.Command
		if command == "" {
			command = ir.DefaultSiteDevCommand
		}
		props["dev"] = map[string]any{"command": command, "port": SiteDevPort(ctx.Environment, d)}
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:         siteID(d.Name),
		Type:       TypeSite,
		Properties: props,
	}}}, nil
}

// connectSite derives the public address of the server whose API a site
// calls: on this machine, its loopback URL (D55). The edge needs no
// resource: the server answers the site's origin through its CORS field.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.SiteEndpoint{URL: ctx.To.PublicAddress}}, nil
}

// connectSQL derives the connection string of the edge's database on the
// environment's Postgres container. The edge needs no resource: the
// container trusts every connection from loopback.
func connectSQL(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{
		URL: DatabaseURL(PostgresPort(ctx.Environment), DatabaseName(ctx.Edge.Service.Name)),
	}}, nil
}

// connectHTTP derives the callee's loopback URL and the service
// credential the caller sends it (D37): a token signed with the edge's
// Ed25519 key, whose iss and sub are the caller's deployable and whose aud
// is the callee's. The edge's key pair is a node the provisioner generates
// into the environment's state directory, so the derived key is a
// reference to its private key, which the provisioner resolves when it
// starts the caller and which no file under the output root holds.
//
// The callee gets the edge's public key, a reference to the node's
// publicJwk output: the caller is an issuer of its own, named by its
// deployable, whose tokens carry the callee's deployable as their audience
// and live at most SignedTokenLifetime seconds.
//
// A server that calls an API it serves itself reaches it on its own
// loopback URL with no edge key, as on every target: the server signs the
// call with a loopback key of its own, which only its process holds
// (docs/stack-model.md, section 9.9).
func connectHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if from.Name == to.Name {
		return registry.Connected{Value: ir.ServiceEndpoint{URL: to.Address}}, nil
	}
	key := keyPairID(from.Name, to.Name)
	serves := make([]string, len(from.Services))
	for i, ref := range from.Services {
		serves[i] = ref.Name
	}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   key,
			Type: TypeKeyPair,
			Properties: map[string]any{
				"caller":    from.Name,
				"callee":    to.Name,
				"algorithm": KeyAlgorithm,
			},
			Phase: ir.PhaseInfrastructure,
		}},
		Value: ir.ServiceEndpoint{
			URL: to.Address,
			Credential: &ir.ServiceCredential{
				Source:   ir.CredentialSignedToken,
				Audience: to.Name,
				Issuer:   from.Name,
				Key:      ir.Output{Resource: key, Name: "privateJwk"},
			},
		},
		Callee: ir.ServiceAuthIssuer{
			Issuer:             from.Name,
			Audience:           to.Name,
			Algorithms:         []string{ir.AlgorithmEdDSA},
			Keys:               []ir.ServiceAuthKey{{JWK: ir.Output{Resource: key, Name: "publicJwk"}}},
			MaxLifetimeSeconds: SignedTokenLifetime,
			Callers:            []ir.ServiceAuthCaller{{Subject: from.Name, Deployable: from.Name, Serves: serves}},
		},
	}, nil
}

// keyPairID is the ID of the key pair of the http edges from one server to
// another. Two edges between the same servers, to two APIs the callee
// serves, share it: the caller is one issuer to the callee.
func keyPairID(caller, callee string) string { return caller + ".calls." + callee + ".key" }

// checkNoDomain refuses a domain: a local server is reached on loopback,
// and no DNS platform writes records for it.
func checkNoDomain(env *ir.ResolvedEnvironment) []string {
	if env.Domain == "" {
		return nil
	}
	return []string{fmt.Sprintf("environment %s sets domain %s, but the local target reaches every server at %s on its own port and writes no DNS records; leave the domain to an environment on a cloud target", env.Environment, env.Domain, Loopback)}
}

// checkNoParameters refuses a parameterized environment.
func checkNoParameters(env *ir.ResolvedEnvironment) []string {
	if len(env.Parameters) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("environment %s takes parameters %s, but the local target runs one copy of each environment; give a parameterized environment a cloud target", env.Environment, strings.Join(env.Parameters, ", "))}
}

// checkDistinctPorts refuses two listeners on one port: two processes, a
// process and a container, or two containers, such as the Postgres
// container, the storage emulator's (D54) and the memory store's (D75). Each container is named by
// its name.
func checkDistinctPorts(env *ir.ResolvedEnvironment) []string {
	owners := map[int][]string{}
	for _, res := range env.Resources.Resources {
		switch res.Type {
		case TypeProcess:
			if port, ok := intValue(res.Properties["port"]); ok {
				owners[port] = append(owners[port], "server "+strings.Join(res.Owners, ", "))
			}
		case TypeSite:
			if port, ok := intValue(res.Properties["port"]); ok {
				owners[port] = append(owners[port], "site "+strings.Join(res.Owners, ", "))
			}
			if dev, _ := res.Properties["dev"].(map[string]any); dev != nil {
				if port, ok := intValue(dev["port"]); ok {
					owners[port] = append(owners[port], "the dev server of site "+strings.Join(res.Owners, ", "))
				}
			}
		case TypeContainer:
			name, _ := res.Properties["name"].(string)
			if name == "" {
				name = res.ID
			}
			ports, _ := res.Properties["ports"].([]any)
			for _, p := range ports {
				m, _ := p.(map[string]any)
				if port, ok := intValue(m["hostPort"]); ok {
					owners[port] = append(owners[port], "container "+name)
				}
			}
		}
	}
	ports := make([]int, 0, len(owners))
	for port := range owners {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	var out []string
	for _, port := range ports {
		if names := slices.Compact(owners[port]); len(names) > 1 {
			out = append(out, fmt.Sprintf("%s listen on one port, %d; give one another with a server's or a site's port setting, a site's site.dev.port, or the environment's postgresPort, storagePort or memoryStorePort value", strings.Join(names, " and "), port))
		}
	}
	return out
}
