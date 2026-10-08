package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func runPlatform(name string) PlatformSpec {
	return PlatformSpec{
		Name:      name,
		Kind:      ir.DeployableServer,
		Languages: []string{APILanguageGo},
		NameOf:    func(PlatformContext) any { return "x" },
		AddressOf: func(PlatformContext) any { return "x" },
		Lower:     func(PlatformContext) (Lowered, error) { return Lowered{}, nil },
	}
}

func sqlPlatform(name string) PlatformSpec {
	spec := runPlatform(name)
	spec.Kind = ir.DeployableDatabase
	spec.Languages = nil
	spec.Dialects = []string{SQLDialectPostgres}
	return spec
}

func connect(ConnectorContext) (Connected, error) { return Connected{Value: "x"}, nil }

type nopProvisioner struct{}

func (nopProvisioner) Render(*ir.ResolvedEnvironment, string) error { return nil }
func (nopProvisioner) Plan(context.Context, ProvisionRequest) ([]PlannedChange, error) {
	return nil, nil
}
func (nopProvisioner) Apply(context.Context, ProvisionRequest, ir.DeployStep) error { return nil }
func (nopProvisioner) Destroy(context.Context, ProvisionRequest) error              { return nil }
func (nopProvisioner) Outputs(context.Context, ProvisionRequest) (map[string]map[string]any, error) {
	return nil, nil
}

// TestRegisterPlatformRejects covers each refusal of RegisterPlatform.
func TestRegisterPlatformRejects(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PlatformSpec)
		want string
	}{
		{"no name", func(s *PlatformSpec) { s.Name = "" }, "has no name"},
		{"malformed name", func(s *PlatformSpec) { s.Name = "Cloud Run" }, "must be lowercase words"},
		{"unknown kind", func(s *PlatformSpec) { s.Kind = "queue" }, `deployable kind "queue"`},
		{"server without languages", func(s *PlatformSpec) { s.Languages = nil }, "declares no languages"},
		{"server with dialects", func(s *PlatformSpec) { s.Dialects = []string{SQLDialectPostgres} }, "only a database platform does"},
		{"unknown language", func(s *PlatformSpec) { s.Languages = []string{"go"} }, `unknown language "go"`},
		{"repeated language", func(s *PlatformSpec) { s.Languages = []string{APILanguageGo, APILanguageGo} }, "twice"},
		{"no Lower", func(s *PlatformSpec) { s.Lower = nil }, "needs NameOf, AddressOf and Lower"},
		{"no NameOf", func(s *PlatformSpec) { s.NameOf = nil }, "needs NameOf, AddressOf and Lower"},
		{"bad settings schema", func(s *PlatformSpec) { s.Settings = json.RawMessage(`{"type": 7}`) }, "Settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := runPlatform("fake.run")
			tc.edit(&spec)
			err := New(naming.Default()).RegisterPlatform(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterPlatform = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	t.Run("database without dialects", func(t *testing.T) {
		spec := sqlPlatform("fake.sql")
		spec.Dialects = nil
		if err := New(naming.Default()).RegisterPlatform(spec); err == nil || !strings.Contains(err.Error(), "declares no SQL dialects") {
			t.Fatalf("RegisterPlatform = %v", err)
		}
	})
	t.Run("unknown dialect", func(t *testing.T) {
		spec := sqlPlatform("fake.sql")
		spec.Dialects = []string{"mysql"}
		if err := New(naming.Default()).RegisterPlatform(spec); err == nil || !strings.Contains(err.Error(), `unknown SQL dialect "mysql"`) {
			t.Fatalf("RegisterPlatform = %v", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		reg := New(naming.Default())
		if err := reg.RegisterPlatform(runPlatform("fake.run")); err != nil {
			t.Fatal(err)
		}
		if err := reg.RegisterPlatform(sqlPlatform("fake.run")); err == nil || !strings.Contains(err.Error(), "already registered") {
			t.Fatalf("second RegisterPlatform = %v", err)
		}
	})
}

// TestPlatformValidatesSettings: a platform with a settings schema checks
// settings against it, and one without accepts none.
func TestPlatformValidatesSettings(t *testing.T) {
	reg := New(naming.Default())
	spec := runPlatform("fake.run")
	spec.Settings = json.RawMessage(`{"type": "object", "properties": {"minInstances": {"type": "integer"}}, "additionalProperties": false}`)
	if err := reg.RegisterPlatform(spec); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterPlatform(sqlPlatform("fake.sql")); err != nil {
		t.Fatal(err)
	}
	run, _ := reg.Platform("fake.run")
	if err := run.ValidateSettings(map[string]any{"minInstances": float64(2)}); err != nil {
		t.Errorf("valid settings: %v", err)
	}
	if err := run.ValidateSettings(nil); err != nil {
		t.Errorf("no settings: %v", err)
	}
	err := run.ValidateSettings(map[string]any{"minInstance": float64(2)})
	if err == nil || !strings.Contains(err.Error(), "minInstance") || strings.Contains(err.Error(), "\n") {
		t.Errorf("misspelt key: %v, want one line naming it", err)
	}
	sql, _ := reg.Platform("fake.sql")
	if err := sql.ValidateSettings(map[string]any{"tier": "large"}); err == nil || !strings.Contains(err.Error(), "takes no settings") {
		t.Errorf("settings on a platform without a schema: %v", err)
	}
}

// TestRegisterConnectorRejects covers each refusal of RegisterConnector,
// and Finalize's checks of the platforms it joins.
func TestRegisterConnectorRejects(t *testing.T) {
	valid := ConnectorSpec{Name: "run-sql", Edge: ir.EdgeSQL, From: "fake.run", To: "fake.sql", Connect: connect}
	cases := []struct {
		name string
		edit func(*ConnectorSpec)
		want string
	}{
		{"no name", func(s *ConnectorSpec) { s.Name = "" }, "has no name"},
		{"unknown edge", func(s *ConnectorSpec) { s.Edge = "grpc" }, `edge kind "grpc"`},
		{"no platform", func(s *ConnectorSpec) { s.To = "" }, "needs a From and a To"},
		{"no Connect", func(s *ConnectorSpec) { s.Connect = nil }, "has no Connect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.edit(&spec)
			err := New(naming.Default()).RegisterConnector(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterConnector = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	t.Run("duplicate name and duplicate pair", func(t *testing.T) {
		reg := New(naming.Default())
		if err := reg.RegisterConnector(valid); err != nil {
			t.Fatal(err)
		}
		if err := reg.RegisterConnector(valid); err == nil || !strings.Contains(err.Error(), "already registered") {
			t.Errorf("same name: %v", err)
		}
		other := valid
		other.Name = "run-sql-2"
		if err := reg.RegisterConnector(other); err == nil || !strings.Contains(err.Error(), "both connect fake.run to fake.sql over sql") {
			t.Errorf("same edge and platforms: %v", err)
		}
	})
	finalize := func(t *testing.T, platforms []PlatformSpec, spec ConnectorSpec) error {
		t.Helper()
		reg := New(naming.Default())
		for _, p := range platforms {
			if err := reg.RegisterPlatform(p); err != nil {
				t.Fatal(err)
			}
		}
		if err := reg.RegisterConnector(spec); err != nil {
			t.Fatal(err)
		}
		return finalizeStack(reg)
	}
	t.Run("unregistered platform", func(t *testing.T) {
		err := finalize(t, []PlatformSpec{runPlatform("fake.run")}, valid)
		if err == nil || !strings.Contains(err.Error(), `connector "run-sql" To names platform "fake.sql", which is not registered`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("wrong kind at an end", func(t *testing.T) {
		err := finalize(t, []PlatformSpec{runPlatform("fake.run"), runPlatform("fake.sql")}, valid)
		if err == nil || !strings.Contains(err.Error(), `names platform "fake.sql", a server platform; a sql edge's to is a database`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("sound", func(t *testing.T) {
		if err := finalize(t, []PlatformSpec{runPlatform("fake.run"), sqlPlatform("fake.sql")}, valid); err != nil {
			t.Fatal(err)
		}
	})
	// A job's edges are its API's, so a connector runs from a job platform
	// too (D52), and from nothing else but a server platform.
	jobPlatform := runPlatform("fake.job")
	jobPlatform.Kind = ir.DeployableJob
	fromJob := valid
	fromJob.Name, fromJob.From = "job-sql", "fake.job"
	t.Run("from a job platform", func(t *testing.T) {
		if err := finalize(t, []PlatformSpec{jobPlatform, sqlPlatform("fake.sql")}, fromJob); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("from a database platform", func(t *testing.T) {
		fromDB := valid
		fromDB.Name, fromDB.From = "sql-sql", "fake.other"
		err := finalize(t, []PlatformSpec{sqlPlatform("fake.other"), sqlPlatform("fake.sql")}, fromDB)
		if err == nil || !strings.Contains(err.Error(), `names platform "fake.other", a database platform; a sql edge's from is a server or a job`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("a job platform without languages", func(t *testing.T) {
		spec := jobPlatform
		spec.Languages = nil
		if err := New(naming.Default()).RegisterPlatform(spec); err == nil || !strings.Contains(err.Error(), `job platform "fake.job" declares no languages`) {
			t.Fatalf("RegisterPlatform = %v", err)
		}
	})
}

// finalizeStack runs the stack part of Finalize, which the other parts of
// Finalize would refuse first in a registry with no core generators.
func finalizeStack(reg *Registry) error {
	return reg.checkStackReferences()
}

// TestRegisterTargetRejects covers each refusal of RegisterTarget, and
// Finalize's checks of what a target names.
func TestRegisterTargetRejects(t *testing.T) {
	valid := func() TargetSpec {
		return TargetSpec{
			Name:      "fake",
			Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "fake.run", ir.DeployableDatabase: "fake.sql"},
		}
	}
	cases := []struct {
		name string
		edit func(*TargetSpec)
		want string
	}{
		{"no name", func(s *TargetSpec) { s.Name = "" }, "has no name"},
		{"unknown kind", func(s *TargetSpec) { s.Platforms["queue"] = "fake.queue" }, `deployable kind "queue"`},
		{"empty platform", func(s *TargetSpec) { s.Platforms[ir.DeployableServer] = "" }, "empty platform for server"},
		{"bad values schema", func(s *TargetSpec) { s.Values = json.RawMessage(`{"required": 1}`) }, "Values"},
		{"bad resource type schema", func(s *TargetSpec) {
			s.ResourceTypes = map[string]json.RawMessage{"fake:x/y:Z": json.RawMessage(`{"type": 1}`)}
		}, "resource type fake:x/y:Z"},
		{"policy without Check", func(s *TargetSpec) { s.Policies = []PolicyRule{{Name: "ha"}} }, "policy rule without a name or a Check"},
		{"repeated policy", func(s *TargetSpec) {
			check := func(*ir.ResolvedEnvironment) []string { return nil }
			s.Policies = []PolicyRule{{Name: "ha", Check: check}, {Name: "ha", Check: check}}
		}, `two policy rules named "ha"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid()
			tc.edit(&spec)
			err := New(naming.Default()).RegisterTarget(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterTarget = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		reg := New(naming.Default())
		if err := reg.RegisterTarget(valid()); err != nil {
			t.Fatal(err)
		}
		if err := reg.RegisterTarget(valid()); err == nil || !strings.Contains(err.Error(), "already registered") {
			t.Fatalf("second RegisterTarget = %v", err)
		}
	})
	t.Run("resource type with two schemas", func(t *testing.T) {
		reg := New(naming.Default())
		a := valid()
		a.ResourceTypes = map[string]json.RawMessage{"fake:x/y:Z": json.RawMessage(`{"type": "object"}`)}
		if err := reg.RegisterTarget(a); err != nil {
			t.Fatal(err)
		}
		same := valid()
		same.Name = "same"
		same.ResourceTypes = map[string]json.RawMessage{"fake:x/y:Z": json.RawMessage(`{ "type" : "object" }`)}
		if err := reg.RegisterTarget(same); err != nil {
			t.Errorf("the same schema, spelt differently: %v", err)
		}
		other := valid()
		other.Name = "other"
		other.ResourceTypes = map[string]json.RawMessage{"fake:x/y:Z": json.RawMessage(`{"type": "array"}`)}
		if err := reg.RegisterTarget(other); err == nil || !strings.Contains(err.Error(), `target "fake" and target "other" register different schemas for resource type fake:x/y:Z`) {
			t.Errorf("a different schema: %v", err)
		}
	})

	finalize := func(t *testing.T, edit func(*Registry, *TargetSpec)) error {
		t.Helper()
		reg := New(naming.Default())
		for _, p := range []PlatformSpec{runPlatform("fake.run"), sqlPlatform("fake.sql")} {
			if err := reg.RegisterPlatform(p); err != nil {
				t.Fatal(err)
			}
		}
		spec := valid()
		edit(reg, &spec)
		if err := reg.RegisterTarget(spec); err != nil {
			t.Fatal(err)
		}
		return finalizeStack(reg)
	}
	t.Run("unknown platform", func(t *testing.T) {
		err := finalize(t, func(_ *Registry, s *TargetSpec) { s.Platforms[ir.DeployableServer] = "fake.workers" })
		if err == nil || !strings.Contains(err.Error(), `target "fake" names platform "fake.workers" for server, which is not registered`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("platform of another kind", func(t *testing.T) {
		err := finalize(t, func(_ *Registry, s *TargetSpec) { s.Platforms[ir.DeployableDatabase] = "fake.run" })
		if err == nil || !strings.Contains(err.Error(), `names platform "fake.run" for database, but it is a server platform`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("unknown DNS platform", func(t *testing.T) {
		err := finalize(t, func(_ *Registry, s *TargetSpec) { s.DNS = "fake.dns" })
		if err == nil || !strings.Contains(err.Error(), `names DNS platform "fake.dns", which is not registered`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("unknown provisioner", func(t *testing.T) {
		err := finalize(t, func(_ *Registry, s *TargetSpec) { s.Provisioner = "pulumi" })
		if err == nil || !strings.Contains(err.Error(), `names provisioner "pulumi", which is not registered`) {
			t.Fatalf("Finalize = %v", err)
		}
	})
	t.Run("sound", func(t *testing.T) {
		err := finalize(t, func(reg *Registry, s *TargetSpec) {
			if err := reg.RegisterDNSPlatform(DNSPlatformSpec{Name: "fake.dns", Lower: func(DNSContext) ([]*ir.Resource, error) { return nil, nil }}); err != nil {
				t.Fatal(err)
			}
			if err := reg.RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}}); err != nil {
				t.Fatal(err)
			}
			s.DNS, s.Provisioner = "fake.dns", "fake"
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestRegisterDNSPlatformAndProvisionerReject covers the refusals of
// RegisterDNSPlatform and RegisterProvisioner.
func TestRegisterDNSPlatformAndProvisionerReject(t *testing.T) {
	lower := func(DNSContext) ([]*ir.Resource, error) { return nil, nil }
	for _, tc := range []struct {
		name string
		spec DNSPlatformSpec
		want string
	}{
		{"no name", DNSPlatformSpec{Lower: lower}, "has no name"},
		{"reserved name", DNSPlatformSpec{Name: ir.ManualDNS, Lower: lower}, "is reserved"},
		{"no Lower", DNSPlatformSpec{Name: "fake.dns"}, "has no Lower"},
		{"bad values schema", DNSPlatformSpec{Name: "fake.dns", Lower: lower, Values: json.RawMessage(`{"type": []}`)}, "Values"},
	} {
		t.Run("dns "+tc.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterDNSPlatform(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterDNSPlatform = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	reg := New(naming.Default())
	if err := reg.RegisterDNSPlatform(DNSPlatformSpec{Name: "fake.dns", Lower: lower}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDNSPlatform(DNSPlatformSpec{Name: "fake.dns", Lower: lower}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate DNS platform: %v", err)
	}
	if err := reg.RegisterProvisioner(ProvisionerSpec{Name: "fake"}); err == nil || !strings.Contains(err.Error(), "has no Provisioner") {
		t.Errorf("nil provisioner: %v", err)
	}
	if err := reg.RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate provisioner: %v", err)
	}
}

// TestStackSpecsFailAfterFinalize: like every Register*, the stack specs
// refuse a registration once the registry is fixed, and Finalize runs the
// stack checks.
func TestStackSpecsFailAfterFinalize(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterTarget(TargetSpec{Name: "fake", Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "fake.run"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig", "stack"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err == nil || !strings.Contains(err.Error(), `target "fake" names platform "fake.run"`) {
		t.Fatalf("Finalize = %v, want the target's unknown platform", err)
	}
	reg = New(naming.Default())
	finalizeWithCoreGenerators(t, reg)
	for what, err := range map[string]error{
		"platform":     reg.RegisterPlatform(runPlatform("fake.run")),
		"connector":    reg.RegisterConnector(ConnectorSpec{Name: "c", Edge: ir.EdgeHTTP, From: "a", To: "b", Connect: connect}),
		"target":       reg.RegisterTarget(TargetSpec{Name: "fake"}),
		"DNS platform": reg.RegisterDNSPlatform(DNSPlatformSpec{Name: "fake.dns"}),
		"provisioner":  reg.RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}}),
	} {
		if err == nil || !strings.Contains(err.Error(), "after Finalize") {
			t.Errorf("%s after Finalize: %v", what, err)
		}
	}
}

// TestValidateResource checks properties against the schema a target
// registered for the type, with references read as strings.
func TestValidateResource(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterTarget(TargetSpec{Name: "fake", ResourceTypes: map[string]json.RawMessage{
		"fake:iam/grant:Grant": json.RawMessage(`{"type": "object", "required": ["role", "member"],
		  "properties": {"role": {"type": "string"}, "member": {"type": "string"}}, "additionalProperties": false}`),
	}}); err != nil {
		t.Fatal(err)
	}
	ok, err := reg.ValidateResource(&ir.Resource{ID: "g", Type: "fake:iam/grant:Grant", Properties: map[string]any{
		"role":   "run.invoker",
		"member": ir.Concat{"serviceAccount:", ir.Output{Resource: "a", Name: "email"}},
	}})
	if !ok || err != nil {
		t.Errorf("valid grant: ok %v, %v", ok, err)
	}
	if _, err := reg.ValidateResource(&ir.Resource{ID: "g", Type: "fake:iam/grant:Grant", Properties: map[string]any{"role": "x"}}); err == nil || !strings.Contains(err.Error(), "member") {
		t.Errorf("missing member: %v", err)
	}
	if ok, _ := reg.ValidateResource(&ir.Resource{ID: "x", Type: "fake:other:Thing"}); ok {
		t.Error("a type no target registered reports a schema")
	}
}

// TestDNSPlatformResourceTypes: a DNS platform registers the schemas of
// the types it emits as a target does, against the same rule: one schema
// per type, whoever registers it.
func TestDNSPlatformResourceTypes(t *testing.T) {
	lower := func(DNSContext) ([]*ir.Resource, error) { return nil, nil }
	record := json.RawMessage(`{"type": "object", "required": ["zoneId"], "properties": {"zoneId": {"type": "string"}}, "additionalProperties": false}`)
	reg := New(naming.Default())
	if err := reg.RegisterDNSPlatform(DNSPlatformSpec{
		Name: "other.dns", Lower: lower, ResourceTypes: map[string]json.RawMessage{"other:dns/record:Record": record},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := reg.ValidateResource(&ir.Resource{ID: "r", Type: "other:dns/record:Record", Properties: map[string]any{"zoneId": "z"}}); !ok || err != nil {
		t.Errorf("valid record: ok %v, %v", ok, err)
	}
	if _, err := reg.ValidateResource(&ir.Resource{ID: "r", Type: "other:dns/record:Record", Properties: map[string]any{"zone": "z"}}); err == nil {
		t.Error("a record without its zone id validates")
	}
	if spec, _ := reg.DNSPlatform("other.dns"); spec.ResourceTypes != nil {
		t.Error("the registered spec keeps its resource type schemas")
	}
	if err := reg.RegisterTarget(TargetSpec{Name: "fake", ResourceTypes: map[string]json.RawMessage{"other:dns/record:Record": record}}); err != nil {
		t.Errorf("a target with the same schema: %v", err)
	}
	err := reg.RegisterTarget(TargetSpec{Name: "fake2", ResourceTypes: map[string]json.RawMessage{"other:dns/record:Record": json.RawMessage(`{"type": "object"}`)}})
	if err == nil || !strings.Contains(err.Error(), `DNS platform "other.dns" and target "fake2" register different schemas for resource type other:dns/record:Record`) {
		t.Errorf("a target with a different schema: %v", err)
	}
	err = reg.RegisterDNSPlatform(DNSPlatformSpec{Name: "third.dns", Lower: lower, ResourceTypes: map[string]json.RawMessage{"third:x:Y": json.RawMessage(`{"type": []}`)}})
	if err == nil || !strings.Contains(err.Error(), `DNS platform "third.dns" resource type third:x:Y`) {
		t.Errorf("a schema that does not compile: %v", err)
	}
	if _, ok := reg.DNSPlatform("third.dns"); ok {
		t.Error("a refused DNS platform was registered")
	}
	if ok, _ := reg.ValidateResource(&ir.Resource{ID: "x", Type: "third:x:Y"}); ok {
		t.Error("a refused DNS platform's type was registered")
	}
}
