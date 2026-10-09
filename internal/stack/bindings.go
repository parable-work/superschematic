package stack

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// edge is one edge while it is resolved.
type edge struct {
	res       *ir.Edge
	connector registry.ConnectorSpec
	from, to  *deployable
}

// field is one config field of a server.
type field struct {
	name      string
	declaring string
	required  bool
	secret    bool
	def       *string
	// service is the first served service whose config has the field.
	service string
}

// DerivedField returns the name of the config field an edge of kind to
// service fills under the core's rule: the service's name in upper snake
// case, suffixed `_DATABASE` for a sql edge, `_SERVICE` for an http edge
// and `_BUCKET` for a bucket edge (`SHOP_DB_DATABASE`, `SHOP_API_SERVICE`,
// `SHOP_MEDIA_BUCKET`). Input.FieldNames replaces the rule with the naming
// file's (section 3.4).
func DerivedField(kind ir.EdgeKind, service string) string {
	return ir.DerivedFieldNames{}.Field(kind, service)
}

// databaseOf returns the DB service an API service connects to: its
// `authDb`, or its one DB-kind dependency, as resolveUpstreamAuth in
// internal/generator/dispatch.go reads it (section 3.3).
func (r *resolver) databaseOf(svc *Service) (string, bool) {
	if svc.AuthDB != nil {
		db, ok := r.services[svc.AuthDB.Name]
		return svc.AuthDB.Name, ok && db.Kind == ir.SchemaKindDB && svc.AuthDB.Kind == db.Kind
	}
	var dbs []string
	for _, dep := range svc.Dependencies {
		if db, ok := r.services[dep.Name]; ok && dep.Kind == ir.SchemaKindDB && db.Kind == ir.SchemaKindDB {
			dbs = append(dbs, dep.Name)
		}
	}
	switch len(dbs) {
	case 0:
		return "", false
	case 1:
		return dbs[0], true
	}
	r.fail(CodeAmbiguousDatabase, "service %s depends on DB services %s and sets no authDb to name its database", svc.Name, strings.Join(dbs, ", "))
	return "", false
}

// deriveEdges finds every edge: a sql edge from each server to the
// database of each API it serves, an http edge from each server to the
// server of each API it calls, and a bucket edge from each server to each
// bucket an API it serves lists (D54). A job takes its API's edges, from
// itself (D52). Each finds the connector between the two platforms.
func (r *resolver) deriveEdges() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if !d.res.Kind.HasImage() {
			continue
		}
		for _, served := range d.res.Services {
			if db, ok := r.databaseOf(r.services[served.Name]); ok {
				r.addEdge(ir.EdgeSQL, d, db)
			}
		}
		for _, callee := range d.res.Calls {
			r.addEdge(ir.EdgeHTTP, d, callee.Name)
		}
		for _, bucket := range d.res.Buckets {
			r.addEdge(ir.EdgeBucket, d, bucket.Name)
		}
	}
}

func (r *resolver) addEdge(kind ir.EdgeKind, from *deployable, service string) {
	id := ir.EdgeID(kind, from.res.Name, service)
	if _, dup := r.edges[id]; dup {
		return
	}
	to := r.deployables[r.byService[service]]
	svc := r.services[service]
	e := &edge{
		res: &ir.Edge{
			ID:      id,
			Kind:    kind,
			From:    from.res.Name,
			To:      to.res.Name,
			Service: ir.ServiceRef{Name: svc.Name, Kind: svc.Kind},
			Field:   r.in.FieldNames.Field(kind, service),
		},
		from: from,
		to:   to,
	}
	r.edges[id] = e
	connector, ok := r.reg.Connector(kind, from.res.Platform, to.res.Platform)
	if !ok {
		r.fail(CodeNoConnector, "edge %s: no connector from %s to %s over %s", id, from.res.Platform, to.res.Platform, kind)
		return
	}
	e.connector = connector
	e.res.Connector = connector.Name
}

// edgesFrom returns the edges from a deployable, sorted by ID.
func (r *resolver) edgesFrom(name string) []*edge {
	var out []*edge
	for _, id := range sortedKeys(r.edges) {
		if r.edges[id].res.From == name {
			out = append(out, r.edges[id])
		}
	}
	return out
}

// bindConfig binds every config field of every server and job: a secret
// for a `Secret<T>` field, the environment's literal or parameter, the
// field's default, or the value its edge's connector derives (section
// 5.1). A job's fields are its API's, and so are its edges (D52); it
// serves no request, so it has no callers field.
func (r *resolver) bindConfig() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if !d.res.Kind.HasImage() {
			for _, key := range sortedKeys(d.settings.env) {
				r.fail(CodeUnknownEnvKey, "environment %s sets env %s on %s %s, which has no config", d.settings.envFrom[key], key, d.res.Kind, name)
			}
			continue
		}
		r.collectFields(d)
		derived := map[string]*edge{}
		for _, e := range r.edgesFrom(name) {
			if other, clash := derived[e.res.Field]; clash {
				r.fail(CodeFieldCollision, "%s %s: edges %s and %s derive the same field %s", d.res.Kind, name, other.res.ID, e.res.ID, e.res.Field)
			}
			derived[e.res.Field] = e
			for _, fieldName := range sortedKeys(d.fields) {
				if f := d.fields[fieldName]; ir.DerivedFieldClaims(e.res.Field, f.name) {
					r.fail(CodeFieldCollision, "%s %s: config field %s of %s collides with %s, the field edge %s derives", d.res.Kind, name, f.name, f.declaring, e.res.Field, e.res.ID)
				}
			}
		}
		var callers map[string]string
		refused := map[string]bool{}
		if d.res.Kind == ir.DeployableServer {
			callers = r.callersFields(d)
			refused = r.checkCallersFields(d, callers, derived)
		}
		for _, key := range sortedKeys(d.settings.env) {
			if _, ok := d.fields[key]; ok || refused[key] {
				continue
			}
			if d.settings.inherited[key] {
				// The job's API's server takes it for another API it
				// serves, and the server's own binding checked it.
				continue
			}
			if e, ok := derived[key]; ok {
				r.fail(CodeUnknownEnvKey, "environment %s sets env %s on %s %s, the field edge %s derives", d.settings.envFrom[key], key, d.res.Kind, name, e.res.ID)
				continue
			}
			r.fail(CodeUnknownEnvKey, "environment %s sets env %s on %s %s, which is not a field of %s", d.settings.envFrom[key], key, d.res.Kind, name, r.configTypes(d))
		}
		var bindings []*ir.Binding
		for _, fieldName := range sortedKeys(d.fields) {
			if b := r.bindField(d, d.fields[fieldName]); b != nil {
				bindings = append(bindings, b)
			}
		}
		for _, e := range r.edgesFrom(name) {
			bindings = append(bindings, &ir.Binding{Field: e.res.Field, Source: ir.BindingDerived, Edge: e.res.ID})
		}
		bindings = append(bindings, r.callersBindings(d, callers)...)
		slices.SortFunc(bindings, func(a, b *ir.Binding) int { return strings.Compare(a.Field, b.Field) })
		d.res.Bindings = bindings
	}
}

// collectFields gathers a server's config fields from the `@envVars` types
// of the APIs it serves. One field declared by one type is one field
// however many served configs include it.
func (r *resolver) collectFields(d *deployable) {
	d.fields = map[string]*field{}
	for _, served := range d.res.Services {
		svc := r.services[served.Name]
		if svc.Config == nil {
			continue
		}
		for _, f := range svc.Config.Fields {
			declaring := f.declaringType(svc.Config.Type)
			if prev, ok := d.fields[f.Name]; ok {
				if prev.declaring != declaring {
					r.fail(CodeFieldCollision, "server %s: config field %s is declared by both %s (service %s) and %s (service %s)", d.res.Name, f.Name, prev.declaring, prev.service, declaring, svc.Name)
				}
				continue
			}
			d.fields[f.Name] = &field{
				name:      f.Name,
				declaring: declaring,
				required:  f.Required,
				secret:    f.Secret,
				def:       f.Default,
				service:   svc.Name,
			}
		}
	}
}

// configTypes names a server's `@envVars` types for an error message.
func (r *resolver) configTypes(d *deployable) string {
	var types []string
	for _, served := range d.res.Services {
		if cfg := r.services[served.Name].Config; cfg != nil && !slices.Contains(types, cfg.Type) {
			types = append(types, cfg.Type)
		}
	}
	if len(types) == 0 {
		return "any @envVars type: it serves none"
	}
	return strings.Join(types, " or ")
}

// bindField binds one config field of a server or a job. A job's value
// that it takes from its API's server was checked with the server's, and a
// field a job leaves unbound its server leaves unbound too, so neither is
// reported twice.
func (r *resolver) bindField(d *deployable, f *field) *ir.Binding {
	value, set := d.settings.env[f.name]
	fail := func(code Code, format string, args ...any) {
		if !d.settings.inherited[f.name] {
			r.fail(code, format, args...)
		}
	}
	switch {
	case f.secret:
		if set {
			fail(CodeSecretLiteral, "environment %s sets %s on %s %s, a Secret<T> field of %s; a secret's value is entered with `stack secrets set`, never written in a file", d.settings.envFrom[f.name], f.name, d.res.Kind, d.res.Name, f.declaring)
			if !d.settings.inherited[f.name] {
				return nil
			}
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingSecret, Secret: ir.SecretID(f.declaring, f.name)}
	case set && value.Parameter != "" && value.Value != nil:
		fail(CodeInvalidSettings, "environment %s sets %s on %s %s to both a value and parameter %s", d.settings.envFrom[f.name], f.name, d.res.Kind, d.res.Name, value.Parameter)
	case set && value.Parameter != "":
		if !slices.Contains(r.env.parameters, value.Parameter) {
			fail(CodeUnknownParameter, "environment %s binds %s on %s %s to parameter %s, which environment %s does not declare", d.settings.envFrom[f.name], f.name, d.res.Kind, d.res.Name, value.Parameter, r.envName())
			return nil
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingParameter, Parameter: value.Parameter}
	case set:
		if !isScalar(value.Value) {
			fail(CodeInvalidSettings, "environment %s sets %s on %s %s to %s; a literal is a string, a number or a boolean", d.settings.envFrom[f.name], f.name, d.res.Kind, d.res.Name, describe(value.Value))
			return nil
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingLiteral, Value: value.Value}
	case f.def != nil:
		return &ir.Binding{Field: f.name, Source: ir.BindingLiteral, Value: *f.def, Default: true}
	case f.required && d.res.Kind == ir.DeployableServer:
		r.fail(CodeUnboundField, "server %s: required config field %s of %s has no binding and no default", d.res.Name, f.name, f.declaring)
	}
	return nil
}

// contractOf names the derived value an edge of kind fills its field
// with, for an error message.
func contractOf(kind ir.EdgeKind) string {
	if kind == ir.EdgeSQL {
		return "database connection (ir.DatabaseConnection)"
	}
	if kind == ir.EdgeBucket {
		return "bucket connection (ir.BucketConnection)"
	}
	return "service endpoint (ir.ServiceEndpoint)"
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, float64, float32, int, int32, int64, json.Number:
		return true
	}
	return false
}

func describe(v any) string {
	if v == nil {
		return "nothing"
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// secrets lists the environment's secrets with the servers that read
// them.
func (r *resolver) secrets() []*ir.StackSecret {
	byID := map[string]*ir.StackSecret{}
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		for _, b := range d.res.Bindings {
			if b.Source != ir.BindingSecret {
				continue
			}
			s, ok := byID[b.Secret]
			if !ok {
				f := d.fields[b.Field]
				s = &ir.StackSecret{ID: b.Secret, Type: f.declaring, Field: f.name}
				byID[b.Secret] = s
			}
			s.Readers = append(s.Readers, name)
		}
	}
	var out []*ir.StackSecret
	for _, id := range sortedKeys(byID) {
		out = append(out, byID[id])
	}
	return out
}

// connectEdges asks each edge's connector for its resources and its
// derived binding's value, which must meet the contract of the edge's
// kind (ir.CheckDerivedValue). Every connector sees the bindings as they
// stood before any connector ran: the derived bindings without values.
func (r *resolver) connectEdges() {
	values := map[string]any{}
	callees := map[string]any{}
	ran := map[string]bool{}
	for _, id := range sortedKeys(r.edges) {
		e := r.edges[id]
		ctx := registry.ConnectorContext{
			Environment: r.stackEnvironment(),
			Edge:        *e.res,
			From:        cloneDeployable(e.from.res),
			To:          cloneDeployable(e.to.res),
		}
		connected, err := e.connector.Connect(ctx)
		if err != nil {
			r.fail(CodeLowering, "connector %s on edge %s: %v", e.connector.Name, id, err)
			continue
		}
		where := fmt.Sprintf("connector %s derives %s with", e.connector.Name, e.res.Field)
		values[id] = r.normalize(where, connected.Value)
		switch {
		case connected.Value == nil:
			r.fail(CodeLowering, "connector %s on edge %s derives no value for %s", e.connector.Name, id, e.res.Field)
		case values[id] != nil:
			if err := ir.CheckDerivedValue(e.res.Kind, values[id]); err != nil {
				r.fail(CodeLowering, "connector %s on edge %s derives a value for %s that is no %s: %v", e.connector.Name, id, e.res.Field, contractOf(e.res.Kind), err)
			}
		}
		r.checkParameters(where, values[id])
		r.produce(id, ir.PhaseInfrastructure, connected.Resources)
		ran[id] = true
		if connected.Callee == nil {
			continue
		}
		where = fmt.Sprintf("connector %s gives the callee of %s", e.connector.Name, id)
		callee := r.normalize(where, connected.Callee)
		switch {
		case e.res.Kind != ir.EdgeHTTP || e.res.From == e.res.To:
			r.fail(CodeLowering, "connector %s on edge %s gives the callee a caller to verify, but only an http edge between two servers has one: a call within one server carries no service credential", e.connector.Name, id)
			continue
		case callee == nil:
			continue
		}
		if err := checkCallee(e, callee); err != nil {
			r.fail(CodeLowering, "connector %s on edge %s gives %s a caller to verify that is no issuer of a callers field (ir.ServiceAuthIssuer): %v", e.connector.Name, id, e.res.To, err)
			continue
		}
		r.checkParameters(where, callee)
		callees[id] = callee
	}
	for _, id := range sortedKeys(r.edges) {
		for _, b := range r.edges[id].from.res.Bindings {
			if b.Edge == id {
				b.Value = values[id]
			}
		}
	}
	r.fillCallers(callees, ran)
}
