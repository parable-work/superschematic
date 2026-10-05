package stack

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

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
// service fills: the service's name in upper snake case, suffixed
// `_DATABASE` for a sql edge and `_SERVICE` for an http edge
// (`SHOP_DB_DATABASE`, `SHOP_API_SERVICE`). It is the core's rule; a naming
// key replaces it when envgen writes the derived fields (section 3.4).
func DerivedField(kind ir.EdgeKind, service string) string {
	var b strings.Builder
	for _, r := range service {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteByte('_')
		}
	}
	switch kind {
	case ir.EdgeSQL:
		b.WriteString("_DATABASE")
	case ir.EdgeHTTP:
		b.WriteString("_SERVICE")
	}
	return b.String()
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
// database of each API it serves, and an http edge from each server to the
// server of each API it calls. Each finds the connector between the two
// platforms.
func (r *resolver) deriveEdges() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableServer {
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
			Field:   DerivedField(kind, service),
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

// bindConfig binds every config field of every server: a secret for a
// `Secret<T>` field, the environment's literal or parameter, the field's
// default, or the value its edge's connector derives (section 5.1).
func (r *resolver) bindConfig() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableServer {
			for _, key := range sortedKeys(d.settings.env) {
				r.fail(CodeUnknownEnvKey, "environment %s sets env %s on %s %s, which has no config", d.settings.envFrom[key], key, d.res.Kind, name)
			}
			continue
		}
		r.collectFields(d)
		derived := map[string]*edge{}
		for _, e := range r.edgesFrom(name) {
			derived[e.res.Field] = e
			if f, clash := d.fields[e.res.Field]; clash {
				r.fail(CodeFieldCollision, "server %s: config field %s of %s has the name of the field edge %s derives", name, f.name, f.declaring, e.res.ID)
			}
		}
		for _, key := range sortedKeys(d.settings.env) {
			if _, ok := d.fields[key]; ok {
				continue
			}
			if e, ok := derived[key]; ok {
				r.fail(CodeUnknownEnvKey, "environment %s sets env %s on server %s, the field edge %s derives", d.settings.envFrom[key], key, name, e.res.ID)
				continue
			}
			r.fail(CodeUnknownEnvKey, "environment %s sets env %s on server %s, which is not a field of %s", d.settings.envFrom[key], key, name, r.configTypes(d))
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

func (r *resolver) bindField(d *deployable, f *field) *ir.Binding {
	value, set := d.settings.env[f.name]
	switch {
	case f.secret:
		if set {
			r.fail(CodeSecretLiteral, "environment %s sets %s on server %s, a Secret<T> field of %s; a secret's value is entered with `stack secrets set`, never written in a file", d.settings.envFrom[f.name], f.name, d.res.Name, f.declaring)
			return nil
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingSecret, Secret: ir.SecretID(f.declaring, f.name)}
	case set && value.Parameter != "" && value.Value != nil:
		r.fail(CodeInvalidSettings, "environment %s sets %s on server %s to both a value and parameter %s", d.settings.envFrom[f.name], f.name, d.res.Name, value.Parameter)
	case set && value.Parameter != "":
		if !slices.Contains(r.env.parameters, value.Parameter) {
			r.fail(CodeUnknownParameter, "environment %s binds %s on server %s to parameter %s, which environment %s does not declare", d.settings.envFrom[f.name], f.name, d.res.Name, value.Parameter, r.envName())
			return nil
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingParameter, Parameter: value.Parameter}
	case set:
		if !isScalar(value.Value) {
			r.fail(CodeInvalidSettings, "environment %s sets %s on server %s to %s; a literal is a string, a number or a boolean", d.settings.envFrom[f.name], f.name, d.res.Name, describe(value.Value))
			return nil
		}
		return &ir.Binding{Field: f.name, Source: ir.BindingLiteral, Value: value.Value}
	case f.def != nil:
		return &ir.Binding{Field: f.name, Source: ir.BindingLiteral, Value: *f.def, Default: true}
	case f.required:
		r.fail(CodeUnboundField, "server %s: required config field %s of %s has no binding and no default", d.res.Name, f.name, f.declaring)
	}
	return nil
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
// derived binding's value.
func (r *resolver) connectEdges() {
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
		if connected.Value == nil {
			r.fail(CodeLowering, "connector %s on edge %s derives no value for %s", e.connector.Name, id, e.res.Field)
		}
		r.checkParameters(fmt.Sprintf("connector %s derives %s with", e.connector.Name, e.res.Field), connected.Value)
		for _, b := range e.from.res.Bindings {
			if b.Edge == id {
				b.Value = connected.Value
			}
		}
		r.produce(id, ir.PhaseInfrastructure, connected.Resources)
	}
}
