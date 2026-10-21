package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A migration job runs one phase of the plans of the DB services one
// database server hosts, then gives the servers that connect to each DB
// service the privileges their APIs need (`superschematic-migrate job`,
// runtime/migrate/README.md, "Jobs"). A deploy writes the job document and
// the plans it names, and a job on the target runs it: on gcp, a Cloud Run
// job that reaches Cloud SQL through the Cloud SQL Go connector, with IAM
// database authentication (D46).

// JobVersion is the version of the job document.
const JobVersion = 1

// Job is a job document.
type Job struct {
	// Version is JobVersion.
	Version int `json:"version"`

	// Phase is the phase of each plan to run: expand, contract or all.
	Phase Phase `json:"phase"`

	// CloudSQL, when set, reaches every database on one Cloud SQL
	// instance through the Cloud SQL Go connector, as an IAM database
	// user. Without it each database names its URL.
	CloudSQL *CloudSQLConnection `json:"cloudSql,omitempty"`

	// Databases are the DB services to migrate, in order.
	Databases []*JobDatabase `json:"databases"`
}

// CloudSQLConnection is how a job reaches a Cloud SQL instance.
type CloudSQLConnection struct {
	// Instance is the instance's connection name, `project:region:name`.
	Instance string `json:"instance"`

	// User is the IAM database user the job connects as: a service
	// account's email without `.gserviceaccount.com`.
	User string `json:"user"`
}

// JobDatabase is one DB service's database in a job.
type JobDatabase struct {
	// Service is the DB service.
	Service string `json:"service"`

	// Database is the database's name on the Cloud SQL instance, with
	// CloudSQL; DatabaseURL its URL without it.
	Database    string `json:"database,omitempty"`
	DatabaseURL string `json:"databaseUrl,omitempty"`

	// Plan is the plan document whose phase to run: a path, or a gs://
	// URL, read relative to the job document's. Empty runs no plan, for a
	// job that only gives the servers that connect their privileges.
	Plan string `json:"plan,omitempty"`

	// Privileges, when set, are the privileges the job gives after the
	// plan's phase. Nil leaves every privilege as it is.
	Privileges *Privileges `json:"privileges,omitempty"`
}

// Privileges are the privileges a job owns on a DB service's database.
type Privileges struct {
	// ReadWrite are the roles that read and write the DB service's
	// tables: the database users of the servers that connect to it. Each
	// gets USAGE on the schemas that hold its objects, SELECT, INSERT,
	// UPDATE and DELETE on its tables, SELECT on its views and USAGE and
	// SELECT on its sequences, and a role the job's user gave privileges
	// on them that is not listed loses them. The runner's state tables are
	// left out, and so is the engine's schema, Engine's. An empty list
	// takes every such privilege back.
	ReadWrite []string `json:"readWrite"`

	// Engine, when set, gives the deployables that open the engine (D58)
	// on the DB service's database its schema, after ReadWrite.
	Engine *EnginePrivileges `json:"engine,omitempty"`
}

// EnginePrivileges are what a job gives the deployables that open the
// engine on a DB service's database, each as a database user of its own:
// a role they share that owns the engine's schema. The engine acts as its
// schema's owner (runtime/engine, "Several instances on one Postgres
// database"), so whatever any of them creates there, at the engine's own
// migrations or a publish, is the role's, and every member may change it
// later.
type EnginePrivileges struct {
	// Schema is the engine's schema, engine unless the servers configure
	// another. The job creates it, owned by Role, when it is missing, and
	// gives it to Role when another role owns it.
	Schema string `json:"schema"`

	// Role owns Schema. The job creates it, a role that cannot log in,
	// when it is missing, and makes its own user a member, which may act
	// as it.
	Role string `json:"role"`

	// Members are the database users of the deployables that open the
	// engine: each is granted Role, and a role the job granted it before
	// that is not listed loses it. An empty list takes every grant back.
	Members []string `json:"members"`
}

// EngineExtensionSchema is the schema a job creates the unaccent extension
// in when the database has none, which the engine's Search needs on
// Postgres: the one the engine itself creates it in, where its user may
// (runtime/engine/rust/storage, fulltext::EXTENSION_SCHEMA).
const EngineExtensionSchema = "superschematic_extensions"

// engineSchemaPattern is a schema the engine takes: lowercase letters,
// digits and _, starting with a letter or _, at most 63 bytes.
var engineSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// cloudSQLInstancePattern is an instance connection name: a project id,
// optionally under a domain, a region and an instance name.
var cloudSQLInstancePattern = regexp.MustCompile(`^([a-z0-9.-]+:)?[a-z][a-z0-9-]*[a-z0-9]:[a-z]+-[a-z]+[0-9]+:[a-z][a-z0-9-]*$`)

// ReadJob decodes and checks a job document: its version, its phase, and
// each database's service and connection, with CloudSQL a name and no URL
// and without it a URL and no name.
func ReadJob(doc []byte) (*Job, error) {
	var job Job
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&job); err != nil {
		return nil, fmt.Errorf("job document: %w", err)
	}
	if job.Version != JobVersion {
		return nil, fmt.Errorf("job document version %d; this runner reads version %d", job.Version, JobVersion)
	}
	switch job.Phase {
	case Expand, Contract, All:
	default:
		return nil, fmt.Errorf("job document: phase %q is not expand, contract or all", job.Phase)
	}
	if c := job.CloudSQL; c != nil {
		if !cloudSQLInstancePattern.MatchString(c.Instance) {
			return nil, fmt.Errorf("job document: Cloud SQL instance %q is not a connection name, project:region:instance", c.Instance)
		}
		if c.User == "" || strings.HasSuffix(c.User, ".gserviceaccount.com") {
			return nil, fmt.Errorf("job document: Cloud SQL user %q: want the IAM database user, a service account's email without .gserviceaccount.com", c.User)
		}
	}
	if len(job.Databases) == 0 {
		return nil, fmt.Errorf("job document: no databases")
	}
	seen := map[string]bool{}
	for _, d := range job.Databases {
		if d == nil || d.Service == "" {
			return nil, fmt.Errorf("job document: a database names no service")
		}
		if seen[d.Service] {
			return nil, fmt.Errorf("job document: service %s is listed twice", d.Service)
		}
		seen[d.Service] = true
		switch {
		case job.CloudSQL != nil && (d.Database == "" || d.DatabaseURL != ""):
			return nil, fmt.Errorf("job document: service %s: a Cloud SQL job names each database by its name, not a URL", d.Service)
		case job.CloudSQL == nil && (d.DatabaseURL == "" || d.Database != ""):
			return nil, fmt.Errorf("job document: service %s: a job without Cloud SQL names each database by its URL", d.Service)
		}
		if p := d.Privileges; p != nil {
			for _, role := range p.ReadWrite {
				if role == "" || strings.ContainsRune(role, 0) {
					return nil, fmt.Errorf("job document: service %s: an empty role", d.Service)
				}
			}
			if i := firstRepeat(p.ReadWrite); i >= 0 {
				return nil, fmt.Errorf("job document: service %s: role %s is listed twice", d.Service, p.ReadWrite[i])
			}
			if err := checkEngine(p.Engine); err != nil {
				return nil, fmt.Errorf("job document: service %s: engine: %w", d.Service, err)
			}
		}
		if d.Plan == "" && d.Privileges == nil {
			return nil, fmt.Errorf("job document: service %s has neither a plan nor privileges", d.Service)
		}
	}
	return &job, nil
}

// checkEngine checks a database's engine privileges: a schema the engine
// takes, other than public and the extensions' schema, a role, and each
// member once, none of them the role.
func checkEngine(e *EnginePrivileges) error {
	if e == nil {
		return nil
	}
	switch {
	case !engineSchemaPattern.MatchString(e.Schema) || strings.HasPrefix(e.Schema, "pg_"):
		return fmt.Errorf("schema %q is not one the engine takes: lowercase letters, digits and _, starting with a letter or _, at most 63 bytes, and not pg_", e.Schema)
	case e.Schema == "public" || e.Schema == "information_schema" || e.Schema == EngineExtensionSchema:
		return fmt.Errorf("schema %s is not the engine's own", e.Schema)
	case e.Role == "" || strings.ContainsRune(e.Role, 0):
		return fmt.Errorf("no role")
	}
	for _, member := range e.Members {
		switch {
		case member == "" || strings.ContainsRune(member, 0):
			return fmt.Errorf("an empty member")
		case member == e.Role:
			return fmt.Errorf("role %s is its own member", e.Role)
		}
	}
	if i := firstRepeat(e.Members); i >= 0 {
		return fmt.Errorf("member %s is listed twice", e.Members[i])
	}
	return nil
}

func firstRepeat(list []string) int {
	for i := range list {
		if slices.Contains(list[:i], list[i]) {
			return i
		}
	}
	return -1
}

// Granter is a driver that gives roles the privileges a server's APIs
// need on a database's tables (Privileges), and the deployables that open
// the engine its schema (EnginePrivileges). Package postgres implements
// it.
type Granter interface {
	// GrantReadWrite gives each of roles read and write privileges on the
	// database's objects, and takes them back from every other role the
	// connection's user gave them to. It leaves out what except names.
	GrantReadWrite(ctx context.Context, roles []string, except Except) (*Grants, error)

	// GrantEngine makes the engine's schema its role's, creating either
	// when it is missing, grants the role to each member and takes it back
	// from every other role the connection's user granted it to, and
	// readies the extension the engine's Search needs.
	GrantEngine(ctx context.Context, engine EnginePrivileges) (*EngineGrants, error)
}

// Except is what GrantReadWrite leaves out: tables by name, in any
// schema, and every object of a schema, with the grants on the schema.
type Except struct {
	Tables  []string
	Schemas []string
}

// GrantExcept is what a job's GrantReadWrite leaves out: the runner's
// state tables, and the engine's schema and the extensions' schema when p
// gives the engine its own.
func GrantExcept(p *Privileges) Except {
	except := Except{Tables: StateTables()}
	if p != nil && p.Engine != nil {
		except.Schemas = []string{p.Engine.Schema, EngineExtensionSchema}
	}
	return except
}

// EngineGrants is what GrantEngine changed.
type EngineGrants struct {
	// CreatedRole and CreatedSchema report what it created; OwnedSchema
	// that it gave an existing schema to the role.
	CreatedRole, CreatedSchema, OwnedSchema bool

	// Granted are the members that were given the role, and Revoked the
	// roles it was taken back from.
	Granted, Revoked []string

	// Extension is the schema the unaccent extension lives in, or empty
	// when it could not be created, which ExtensionError says why: the
	// engine opens without it, and only Search's publish fails.
	Extension      string
	ExtensionError string
}

// Grants is what GrantReadWrite changed.
type Grants struct {
	// Objects counts the tables, views and sequences the roles were given
	// privileges on, and Schemas the schemas that hold them.
	Objects int
	Schemas int

	// Revoked are the roles whose privileges were taken back.
	Revoked []string
}

// StateTables are the runner's own tables, which no server is given
// privileges on.
func StateTables() []string { return []string{stateTable, logTable} }
