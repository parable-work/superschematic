package local

import (
	"fmt"
	"hash/fnv"
	"path"
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
)

// A port the environment does not set falls in a range by a hash of the
// stack, the environment and, for a server, the deployable, so it stays
// the same from run to run and differs between environments. The servers'
// range, the Postgres container's and the storage emulator's do not
// overlap, and all sit below the ephemeral ranges of Linux (32768 and up)
// and macOS (49152 and up).
const (
	serverPortBase   = 20000
	storagePortBase  = 23000
	postgresPortBase = 30000
	portSpan         = 2768
)

// hashPort returns base plus a hash of parts, below base+portSpan.
func hashPort(base int, parts ...string) int {
	h := fnv.New32a()
	for i, part := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(part))
	}
	return base + int(h.Sum32()%portSpan)
}

// ServerPort returns the port a server listens on: its `port` setting, or
// one derived from the stack, the environment and the server's name.
func ServerPort(env registry.StackEnvironment, d ir.ResolvedDeployable) int {
	if port, ok := intValue(d.Settings["port"]); ok {
		return port
	}
	return hashPort(serverPortBase, env.Stack, env.Name, d.Name)
}

// PostgresPort returns the host port the environment's Postgres container
// publishes: the `postgresPort` value, or one derived from the stack and
// the environment.
func PostgresPort(env registry.StackEnvironment) int {
	if port, ok := intValue(env.Values["postgresPort"]); ok {
		return port
	}
	return hashPort(postgresPortBase, env.Stack, env.Name)
}

// PostgresImage returns the image the environment's Postgres container
// runs: the `postgresImage` value, or DefaultPostgresImage.
func PostgresImage(env registry.StackEnvironment) string {
	if image, ok := env.Values["postgresImage"].(string); ok && image != "" {
		return image
	}
	return DefaultPostgresImage
}

// ContainerName returns the name of the environment's Postgres container:
// `superschematic-<stack>-<environment>-postgres`, lower case.
func ContainerName(stack, environment string) string {
	return strings.ToLower("superschematic-" + stack + "-" + environment + "-postgres")
}

// StoragePort returns the host port the environment's storage emulator
// publishes: the `storagePort` value, or one derived from the stack and the
// environment (D54).
func StoragePort(env registry.StackEnvironment) int {
	if port, ok := intValue(env.Values["storagePort"]); ok {
		return port
	}
	return hashPort(storagePortBase, env.Stack, env.Name)
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
// emulator's container: `superschematic-<stack>-<environment>-storage`,
// lower case.
func StorageContainerName(stack, environment string) string {
	return strings.ToLower("superschematic-" + stack + "-" + environment + "-storage")
}

// StorageURL returns the base URL of a storage emulator published on port:
// what a bucket edge derives as its endpoint.
func StorageURL(port int) string {
	return fmt.Sprintf("http://%s:%d", Loopback, port)
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

// postgresContainer is the environment's Postgres container node. Every
// database deployable returns the same node, so they share it. It is ready
// once pg_isready answers inside it.
func postgresContainer(env registry.StackEnvironment) *ir.Resource {
	return &ir.Resource{
		ID:   PostgresContainer,
		Type: TypeContainer,
		Properties: map[string]any{
			"name":  ContainerName(env.Stack, env.Name),
			"image": PostgresImage(env),
			"ports": []any{map[string]any{
				"host":          Loopback,
				"hostPort":      PostgresPort(env),
				"containerPort": postgresPort,
			}},
			"env": []any{map[string]any{"name": "POSTGRES_HOST_AUTH_METHOD", "value": "trust"}},
			"labels": map[string]any{
				"superschematic.stack":       env.Stack,
				"superschematic.environment": env.Name,
			},
			"readiness": map[string]any{"exec": []any{
				"pg_isready", "--host", Loopback, "--port", strconv.Itoa(postgresPort), "--username", PostgresUser, "--quiet",
			}},
		},
		Phase: ir.PhaseInfrastructure,
	}
}

// storageContainer is the environment's storage emulator's container node
// (D54): fake-gcs-server on plain HTTP, keeping its objects in the
// container's filesystem, so a stopped container keeps them for the next
// run. The emulator writes the URLs it hands out with the host's view of
// its address. Every bucket deployable returns the same node, so they
// share it. It is ready once it answers its list of buckets.
func storageContainer(env registry.StackEnvironment) *ir.Resource {
	port := StoragePort(env)
	host := Loopback + ":" + strconv.Itoa(port)
	return &ir.Resource{
		ID:   StorageContainer,
		Type: TypeContainer,
		Properties: map[string]any{
			"name":  StorageContainerName(env.Stack, env.Name),
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
			"labels": map[string]any{
				"superschematic.stack":       env.Stack,
				"superschematic.environment": env.Name,
			},
			"readiness": map[string]any{"http": StorageReadinessPath},
		},
		Phase: ir.PhaseInfrastructure,
	}
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
// emulator and the emulator's endpoint, which the runtimes' GCS
// implementation reaches with no credential (D54). The edge needs no
// resource: the emulator checks no caller.
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
			return fmt.Errorf("config field %s is the variable the local platform sets to the server's port; set the port with the server's port setting instead", PortVariable)
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
// as ir.DerivedVariables encodes it (section 3.4). claim refuses a name
// the platform sets itself.
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

// lowerSite lowers a site to its node (D55): its package, relative to the
// output root, the script that builds it and the directory the build
// writes, its single-page fallback, the port its file server listens on,
// and its config, each API it calls with the loopback URL its site edge
// derives. The provisioner builds it once and serves it until the
// environment stops.
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
// loopback URL, with no key and no credential, as on every target.
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
// container and the storage emulator's (D54). Each container is named by
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
			out = append(out, fmt.Sprintf("%s listen on one port, %d; give one another with a server's or a site's port setting or the environment's postgresPort or storagePort value", strings.Join(names, " and "), port))
		}
	}
	return out
}
