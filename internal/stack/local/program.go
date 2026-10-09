package local

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// ProgramFile is the file Render writes into the program directory: what
// the provisioner runs for the environment, where a person can read it.
const ProgramFile = "local.json"

// ProgramVersion is the version of the program file's format.
const ProgramVersion = 1

// ModelsDir is the directory, under the program directory, the deploy
// writes each hosted DB schema's model into before it applies,
// `<service>.json` in canonical JSON, as `superschematic migrate plan
// --print-model` prints it. The provisioner plans each migration to it.
const ModelsDir = "models"

// MigrationsDir is the directory, under the program directory, the
// provisioner writes each migration plan it applies into,
// `<service>.plan.json`.
const MigrationsDir = "migrations"

// binDir is the directory, under the program directory, each Go server's
// binary is built into.
const binDir = "bin"

// Program is what the local provisioner runs for one environment: the
// environment's resource graph read into the containers, databases and
// processes it starts, in deploy order. Render writes it as ProgramFile.
type Program struct {
	Version     int    `json:"version"`
	Stack       string `json:"stack"`
	Environment string `json:"environment"`

	// Containers are the Docker containers, sorted by ID.
	Containers []*Container `json:"containers,omitempty"`

	// Databases are the databases on the containers, sorted by ID.
	Databases []*Database `json:"databases,omitempty"`

	// KeyPairs are the http edges' key pairs, sorted by ID.
	KeyPairs []*KeyPair `json:"keyPairs,omitempty"`

	// Migrations are the DB schemas each migrate step migrates, expand and
	// contract back to back, in deploy order.
	Migrations []*Migration `json:"migrations,omitempty"`

	// Servers are the processes, in deploy order: callees first.
	Servers []*Server `json:"servers,omitempty"`

	// Jobs are the jobs, in deploy order: each after its callees (D52).
	Jobs []*Job `json:"jobs,omitempty"`

	// Sites are the sites, in deploy order: each after the servers it
	// calls (D55).
	Sites []*Site `json:"sites,omitempty"`
}

// Container is a Docker container node.
type Container struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Host          string            `json:"host"`
	HostPort      int               `json:"hostPort"`
	ContainerPort int               `json:"containerPort"`
	Env           []EnvVar          `json:"env,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// Database is a database node: a database on a container.
type Database struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Service   string `json:"service"`
	Container string `json:"container"`
	URL       string `json:"url"`
}

// KeyPair is a key pair node: the Ed25519 key the caller signs its service
// credential with, and whose public half the callee accepts.
type KeyPair struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
	Callee string `json:"callee"`
}

// Migration is one DB schema's migration: the plan from the model its
// database recorded to the model the deploy wrote into ModelsDir.
type Migration struct {
	// Database is the database deployable the migrate step names; Service
	// is the DB schema; Resource is its database node.
	Database string `json:"database"`
	Service  string `json:"service"`
	Resource string `json:"resource"`
	URL      string `json:"url"`
	Model    string `json:"model"`
	Plan     string `json:"plan"`
}

// Server is a process node: the server's entrypoint, started with its
// environment, then probed until it is ready. A Go server's module is
// built into Binary first; a TypeScript server's main.ts runs on Bun, with
// no binary.
type Server struct {
	ID         string   `json:"id"`
	Deployable string   `json:"deployable"`
	Name       string   `json:"name"`
	Wave       int      `json:"wave"`
	Language   string   `json:"language"`
	Module     string   `json:"module"`
	Binary     string   `json:"binary,omitempty"`
	Port       int      `json:"port"`
	URL        string   `json:"url"`
	Readiness  string   `json:"readiness"`
	Env        []EnvVar `json:"env,omitempty"`
}

// Job is a job node (D52): the job's entrypoint module, built with the
// servers and run with its environment on its schedule, in its time zone,
// while the environment runs, or once by `stack run`. A run that outlives
// TimeoutSeconds is stopped, and a failed run is run again up to Retries
// times.
type Job struct {
	ID             string   `json:"id"`
	Deployable     string   `json:"deployable"`
	Name           string   `json:"name"`
	Wave           int      `json:"wave"`
	Module         string   `json:"module"`
	Binary         string   `json:"binary"`
	API            string   `json:"api"`
	Job            string   `json:"job"`
	Schedule       string   `json:"schedule,omitempty"`
	TimeZone       string   `json:"timeZone"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	Retries        int      `json:"retries,omitempty"`
	Env            []EnvVar `json:"env,omitempty"`
}

// EnvVar is one environment variable of a process or a container: a value,
// which may reference a parameter or an output, or the ID of a secret the
// provisioner reads from the environment's secrets file.
type EnvVar struct {
	Name   string `json:"name"`
	Value  any    `json:"value,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// MarshalJSON writes a secret's name and ID, or a value's name and value,
// an empty string included.
func (v EnvVar) MarshalJSON() ([]byte, error) {
	if v.Secret != "" {
		return json.Marshal(struct {
			Name   string `json:"name"`
			Secret string `json:"secret"`
		}{v.Name, v.Secret})
	}
	return json.Marshal(struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}{v.Name, v.Value})
}

// ProgramOf reads the program out of a resolved environment. It is pure.
// It refuses a node of a type the local provider does not have, and a node
// whose properties the program cannot read: what a registered schema let
// through but this provisioner cannot run.
func ProgramOf(env *ir.ResolvedEnvironment) (*Program, error) {
	if env == nil || env.Resources == nil {
		return nil, fmt.Errorf("local: no environment to run")
	}
	prog := &Program{Version: ProgramVersion, Stack: env.Stack, Environment: env.Environment}
	servers := map[string]*Server{}
	jobs := map[string]*Job{}
	sites := map[string]*Site{}
	containers := map[string]*Container{}
	for _, res := range env.Resources.Resources {
		if res.Inherited {
			return nil, fmt.Errorf("local: resource %s is inherited from another environment, which the local provisioner does not run", res.ID)
		}
		var err error
		switch res.Type {
		case TypeContainer:
			var c *Container
			if c, err = containerOf(res); err == nil {
				containers[c.ID] = c
				prog.Containers = append(prog.Containers, c)
			}
		case TypeDatabase:
			var d *Database
			if d, err = databaseOf(res); err == nil {
				prog.Databases = append(prog.Databases, d)
			}
		case TypeKeyPair:
			var k *KeyPair
			if k, err = keyPairOf(res); err == nil {
				prog.KeyPairs = append(prog.KeyPairs, k)
			}
		case TypeProcess:
			var s *Server
			if s, err = serverOf(res); err == nil {
				servers[s.ID] = s
			}
		case TypeJob:
			var j *Job
			if j, err = jobOf(res); err == nil {
				jobs[j.ID] = j
			}
		case TypeSite:
			var s *Site
			if s, err = siteOf(res); err == nil {
				sites[s.ID] = s
			}
		default:
			err = fmt.Errorf("it has type %s, which is not the local provider's", res.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("local: resource %s: %w", res.ID, err)
		}
	}
	for _, db := range prog.Databases {
		c, ok := containers[db.Container]
		if !ok {
			return nil, fmt.Errorf("local: database %s is on container %s, which the environment does not hold", db.ID, db.Container)
		}
		db.Container = c.ID
	}
	for _, step := range env.DeployOrder {
		switch step.Step {
		case ir.StepMigrate:
			if step.Migration != ir.MigrationExpand {
				continue
			}
			for _, name := range step.Deployables {
				d := env.Deployable(name)
				if d == nil {
					return nil, fmt.Errorf("local: a migrate step names %s, which the environment does not hold", name)
				}
				for _, svc := range d.Services {
					db := prog.database(databaseID(name, svc.Name))
					if db == nil {
						return nil, fmt.Errorf("local: database %s hosts %s, but the environment holds no node %s", name, svc.Name, databaseID(name, svc.Name))
					}
					prog.Migrations = append(prog.Migrations, &Migration{
						Database: name,
						Service:  svc.Name,
						Resource: db.ID,
						URL:      db.URL,
						Model:    filepath.ToSlash(filepath.Join(ModelsDir, svc.Name+".json")),
						Plan:     filepath.ToSlash(filepath.Join(MigrationsDir, svc.Name+".plan.json")),
					})
				}
			}
		case ir.StepRollout:
			for _, id := range step.Resources {
				if s, ok := servers[id]; ok {
					s.Wave = step.Wave
					prog.Servers = append(prog.Servers, s)
				}
				if j, ok := jobs[id]; ok {
					j.Wave = step.Wave
					prog.Jobs = append(prog.Jobs, j)
				}
				if s, ok := sites[id]; ok {
					s.Wave = step.Wave
					prog.Sites = append(prog.Sites, s)
				}
			}
		}
	}
	for id := range sites {
		if !slices.ContainsFunc(prog.Sites, func(s *Site) bool { return s.ID == id }) {
			return nil, fmt.Errorf("local: site %s is in no rollout step", id)
		}
	}
	for id := range servers {
		if !slices.ContainsFunc(prog.Servers, func(s *Server) bool { return s.ID == id }) {
			return nil, fmt.Errorf("local: process %s is in no rollout step", id)
		}
	}
	for id := range jobs {
		if !slices.ContainsFunc(prog.Jobs, func(j *Job) bool { return j.ID == id }) {
			return nil, fmt.Errorf("local: job %s is in no rollout step", id)
		}
	}
	return prog, nil
}

// job returns the job node id, or nil.
func (p *Program) job(id string) *Job {
	for _, j := range p.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// JobOf returns the job whose deployable is name, or nil.
func (p *Program) JobOf(name string) *Job {
	for _, j := range p.Jobs {
		if j.Deployable == name {
			return j
		}
	}
	return nil
}

func (p *Program) database(id string) *Database {
	for _, db := range p.Databases {
		if db.ID == id {
			return db
		}
	}
	return nil
}

func (p *Program) container(id string) *Container {
	for _, c := range p.Containers {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (p *Program) keyPair(id string) *KeyPair {
	for _, k := range p.KeyPairs {
		if k.ID == id {
			return k
		}
	}
	return nil
}

func (p *Program) server(id string) *Server {
	for _, s := range p.Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Marshal encodes the program as Render writes it: two-space indentation,
// no HTML escaping, and a final newline.
func (p *Program) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readProgram reads the program Render wrote into dir and checks it is the
// one env renders.
func readProgram(env *ir.ResolvedEnvironment, dir string) (*Program, error) {
	prog, err := ProgramOf(env)
	if err != nil {
		return nil, err
	}
	want, err := prog.Marshal()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("local: the request names no program directory; render environment %s first", env.Environment)
	}
	got, err := os.ReadFile(filepath.Join(dir, ProgramFile))
	if err != nil {
		return nil, fmt.Errorf("local: render environment %s first: %w", env.Environment, err)
	}
	if !bytes.Equal(got, want) {
		return nil, fmt.Errorf("local: %s is not the program of environment %s as it resolves now; render it again", filepath.Join(dir, ProgramFile), env.Environment)
	}
	return prog, nil
}

func containerOf(res *ir.Resource) (*Container, error) {
	c := &Container{ID: res.ID}
	var ok bool
	if c.Name, ok = res.Properties["name"].(string); !ok || c.Name == "" {
		return nil, fmt.Errorf("its name is not a string")
	}
	if c.Image, ok = res.Properties["image"].(string); !ok || c.Image == "" {
		return nil, fmt.Errorf("its image is not a string")
	}
	ports, _ := res.Properties["ports"].([]any)
	if len(ports) != 1 {
		return nil, fmt.Errorf("it publishes %d ports; the local provisioner runs a container that publishes one", len(ports))
	}
	port, _ := ports[0].(map[string]any)
	c.Host, _ = port["host"].(string)
	if c.HostPort, ok = intValue(port["hostPort"]); !ok {
		return nil, fmt.Errorf("its hostPort is not a number")
	}
	if c.ContainerPort, ok = intValue(port["containerPort"]); !ok {
		return nil, fmt.Errorf("its containerPort is not a number")
	}
	if c.Host == "" {
		c.Host = Loopback
	}
	env, err := envOf(res.Properties["env"])
	if err != nil {
		return nil, err
	}
	for _, v := range env {
		if _, literal := v.Value.(string); !literal || v.Secret != "" {
			return nil, fmt.Errorf("container env %s is not a literal string", v.Name)
		}
	}
	c.Env = env
	if labels, ok := res.Properties["labels"].(map[string]any); ok && len(labels) > 0 {
		c.Labels = map[string]string{}
		for key, value := range labels {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("label %s is not a string", key)
			}
			c.Labels[key] = s
		}
	}
	return c, nil
}

func databaseOf(res *ir.Resource) (*Database, error) {
	d := &Database{ID: res.ID}
	var ok bool
	if d.Name, ok = res.Properties["name"].(string); !ok || d.Name == "" {
		return nil, fmt.Errorf("its name is not a string")
	}
	if d.Service, ok = res.Properties["service"].(string); !ok || d.Service == "" {
		return nil, fmt.Errorf("its service is not a string")
	}
	if d.URL, ok = res.Properties["url"].(string); !ok || d.URL == "" {
		return nil, fmt.Errorf("its url is not a string")
	}
	container, ok := res.Properties["container"].(ir.Output)
	if !ok {
		return nil, fmt.Errorf("its container is not a container's output")
	}
	d.Container = container.Resource
	return d, nil
}

func keyPairOf(res *ir.Resource) (*KeyPair, error) {
	k := &KeyPair{ID: res.ID}
	var ok bool
	if k.Caller, ok = res.Properties["caller"].(string); !ok || k.Caller == "" {
		return nil, fmt.Errorf("its caller is not a string")
	}
	if k.Callee, ok = res.Properties["callee"].(string); !ok || k.Callee == "" {
		return nil, fmt.Errorf("its callee is not a string")
	}
	if algorithm, _ := res.Properties["algorithm"].(string); algorithm != KeyAlgorithm {
		return nil, fmt.Errorf("its algorithm is %q, not %s", algorithm, KeyAlgorithm)
	}
	return k, nil
}

func serverOf(res *ir.Resource) (*Server, error) {
	s := &Server{ID: res.ID, Deployable: strings.TrimSuffix(res.ID, ".process")}
	if len(res.Owners) == 1 {
		s.Deployable = res.Owners[0]
	}
	var ok bool
	if s.Name, ok = res.Properties["name"].(string); !ok || s.Name == "" {
		return nil, fmt.Errorf("its name is not a string; a process is named with no parameter")
	}
	if s.Module, ok = res.Properties["module"].(string); !ok || s.Module == "" {
		return nil, fmt.Errorf("its module is not a string")
	}
	switch s.Language, _ = res.Properties["language"].(string); s.Language {
	case LanguageGo:
		s.Binary = filepath.ToSlash(filepath.Join(binDir, s.Name))
	case LanguageTypeScript:
	default:
		return nil, fmt.Errorf("its language is %q; the local provisioner runs %s and %s servers", s.Language, LanguageGo, LanguageTypeScript)
	}
	if s.Port, ok = intValue(res.Properties["port"]); !ok {
		return nil, fmt.Errorf("its port is not a number")
	}
	if s.Readiness, ok = res.Properties["readiness"].(string); !ok || !strings.HasPrefix(s.Readiness, "/") {
		return nil, fmt.Errorf("its readiness path is not a path")
	}
	s.URL = ServerURL(s.Port)
	env, err := envOf(res.Properties["env"])
	if err != nil {
		return nil, err
	}
	s.Env = env
	return s, nil
}

func jobOf(res *ir.Resource) (*Job, error) {
	j := &Job{ID: res.ID, Deployable: strings.TrimSuffix(res.ID, ".job")}
	if len(res.Owners) == 1 {
		j.Deployable = res.Owners[0]
	}
	var ok bool
	if j.Name, ok = res.Properties["name"].(string); !ok || j.Name == "" {
		return nil, fmt.Errorf("its name is not a string; a job is named with no parameter")
	}
	if j.Module, ok = res.Properties["module"].(string); !ok || j.Module == "" {
		return nil, fmt.Errorf("its module is not a string")
	}
	if j.API, ok = res.Properties["api"].(string); !ok || j.API == "" {
		return nil, fmt.Errorf("its api is not a string")
	}
	if j.Job, ok = res.Properties["job"].(string); !ok || j.Job == "" {
		return nil, fmt.Errorf("its job is not a string")
	}
	if schedule, set := res.Properties["schedule"]; set {
		if j.Schedule, ok = schedule.(string); !ok {
			return nil, fmt.Errorf("its schedule is not a string")
		}
	}
	if j.TimeZone, ok = res.Properties["timeZone"].(string); !ok || j.TimeZone == "" {
		return nil, fmt.Errorf("its timeZone is not a string")
	}
	if j.TimeoutSeconds, ok = intValue(res.Properties["timeoutSeconds"]); !ok || j.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("its timeoutSeconds is not a positive number")
	}
	if j.Retries, ok = intValue(res.Properties["retries"]); !ok || j.Retries < 0 {
		return nil, fmt.Errorf("its retries is not a number of zero or more")
	}
	j.Binary = filepath.ToSlash(filepath.Join(binDir, j.Name))
	env, err := envOf(res.Properties["env"])
	if err != nil {
		return nil, err
	}
	j.Env = env
	return j, nil
}

// envOf reads a node's env list.
func envOf(v any) ([]EnvVar, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("its env is not a list")
	}
	out := make([]EnvVar, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("env[%d] is not an object", i)
		}
		name, _ := m["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("env[%d] has no name", i)
		}
		secret, _ := m["secret"].(string)
		value, hasValue := m["value"]
		if (secret == "") == !hasValue {
			return nil, fmt.Errorf("env %s holds a value or a secret, not both or neither", name)
		}
		out = append(out, EnvVar{Name: name, Value: value, Secret: secret})
	}
	return out, nil
}
