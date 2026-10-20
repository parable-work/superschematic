package topcoat

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// procedure is a Topcoat procedure that calls one mounted operation from
// the browser: its arguments as a record, its result as a record, a
// refusal as a ProblemRecord.
type procedure struct {
	Namespace string
	Name      string
	// Route is the procedure's path, stable across builds:
	// /_superschematic/<service>/<namespace>/<operation>.
	Route     string
	SnakeName string
	// ArgsName is the API crate's Args struct and ArgsRecord the record the
	// browser sends it as; both empty for an operation without arguments.
	ArgsName   string
	ArgsRecord string
	Args       []procedureArg
	// Paged marks a @paginated operation (D60): its arguments record takes
	// the page asked for, `cursor` and `limit`, and its result is its page
	// record. Sort is the record's field of a sorted one's sort (D60,
	// amended), named as its argument, empty for an unsorted one.
	Paged bool
	Sort  string
	// Output is the result's record type, "()" without a result, and
	// ReadOutput the expression that reads it from the result's JSON
	// (`json`).
	Output     string
	ReadOutput string
	// The route's traffic controls, which the procedure meets as the route
	// does (ProcedureControls).
	routeControls
}

// routeControls are an operation's route's @rateLimit requests per minute,
// @bodyLimit megabytes and @timeout seconds, 0 without the directive, and
// Const, the API crate's OperationInfo constant of the operation, whose
// limiter the app's registry holds (D44, amended). Crate is the Topcoat
// crate of the API dependency whose operation it is (D61, amended), empty
// for the service's own: its limiter is in that crate's registry, which
// its setup keeps in the app context.
type routeControls struct {
	RateLimit int
	BodyLimit int
	Timeout   int
	Const     string
	Crate     string
}

// key names the operation the controls are of, apart from a dependency's
// of the same name.
func (p routeControls) key() string { return p.Crate + "::" + p.Const }

// LimiterFrom is the Rust of the function that takes a dependency's
// operation's limiter from its crate's setup, at a request (D61, amended),
// none when the app does not set the crate up.
func (p routeControls) LimiterFrom() string {
	return "|cx: &Cx| try_app_context::<" + p.Crate + "::Setup>(cx)?.rate_limits.of(&" + p.Crate + "::api::operations::" + p.Const + ")"
}

func controlsOf(e registry.RustEndpoint) routeControls {
	return routeControls{RateLimit: e.RateLimit, BodyLimit: e.BodyLimit, Timeout: e.Timeout, Const: e.ConstName()}
}

// HasControls reports whether the route has a traffic control, so a layer
// applies it to a procedure or a page that calls the operation.
func (p routeControls) HasControls() bool {
	return p.RateLimit > 0 || p.BodyLimit > 0 || p.Timeout > 0
}

// ControlCalls are the builder calls of the route's controls, in the
// order a request meets them on the route: the rate limit, the
// operation's limiter in the app's registry (`limits`), which its route
// and every procedure and page that calls it share; the body limit; then
// the timeout.
func (p routeControls) ControlCalls() []string {
	var calls []string
	switch {
	case p.RateLimit > 0 && p.Crate != "":
		calls = append(calls, ".rate_limiter_from("+p.LimiterFrom()+")")
	case p.RateLimit > 0:
		calls = append(calls, fmt.Sprintf(".rate_limiter(limits.of(&crate::api::operations::%s))", p.Const))
	}
	if p.BodyLimit > 0 {
		calls = append(calls, fmt.Sprintf(".body_limit_megabytes(%d)", p.BodyLimit))
	}
	if p.Timeout > 0 {
		calls = append(calls, fmt.Sprintf(".timeout_seconds(%d)", p.Timeout))
	}
	return calls
}

// procedureArg is one field of an Args record.
type procedureArg struct {
	// Field is the Args struct's field, RecordField the record's: the same
	// unless a record reserves the name.
	Field       string
	RecordField string
	Name        string // the argument's name, which a refusal names
	Type        string
	Optional    bool
	Put         string
}

// Decode is the expression that turns the record's field into the Args
// field: its JSON, as a request carries it, decoded into the field's Rust
// type.
func (a procedureArg) Decode() string {
	if a.Optional {
		return fmt.Sprintf("decode(%s, self.%s.as_ref().map_or(Value::Null, %s))?", rustString(a.Name), a.RecordField, a.Put)
	}
	return fmt.Sprintf("decode(%s, %s)?", rustString(a.Name), apply(a.Put, "&self."+a.RecordField))
}

// proceduresOf is a procedure per operation of ops, the operations browser
// code may call, its argument and result types added to records.
func proceduresOf(records *recordBuilder, ops []declared, service string) ([]procedure, error) {
	var out []procedure
	for _, d := range ops {
		endpoint, op := d.endpoint, d.op
		p := procedure{
			Namespace:     endpoint.Namespace,
			Name:          endpoint.Name,
			Route:         "/_superschematic/" + service + "/" + kebabCase(endpoint.Namespace) + "/" + kebabCase(endpoint.Name),
			SnakeName:     endpoint.SnakeName(),
			Output:        "()",
			routeControls: controlsOf(endpoint),
		}
		var err error
		if endpoint.HasArgs() {
			p.ArgsName, p.ArgsRecord = endpoint.ArgsName, endpoint.HandlerName+"ArgsRecord"
			if p.Args, err = records.procedureArgs(endpoint, op); err != nil {
				return nil, fmt.Errorf("topcoat: procedure of %s.%s: %w", endpoint.Namespace, endpoint.Name, err)
			}
		}
		switch {
		case endpoint.Page != nil:
			page, err := records.pageOf(d)
			if err != nil {
				return nil, fmt.Errorf("topcoat: procedure of %s.%s: %w", endpoint.Namespace, endpoint.Name, err)
			}
			p.Paged = true
			p.Output, p.ReadOutput = page.Name, page.Name+"::from_wire(&json)"
			if sort := endpoint.Page.Sort; sort != nil {
				p.Sort = recordFieldName(registry.RustIdentifier(sort.Param, "sort"))
			}
		case endpoint.OutputRustType != "()":
			s, err := records.fieldShape(op.TypeRef, false)
			if err != nil {
				return nil, fmt.Errorf("topcoat: procedure of %s.%s: %w", endpoint.Namespace, endpoint.Name, err)
			}
			p.Output, p.ReadOutput = s.rustType, apply(s.read, "&json")
		}
		out = append(out, p)
	}
	return out, nil
}

// procedureArgs are the fields of an operation's Args record: its path,
// query and body arguments, each by its IR type, and its input as the input
// type's record. A field is optional when the Args field is.
func (b *recordBuilder) procedureArgs(endpoint registry.RustEndpoint, op *ir.FieldDef) ([]procedureArg, error) {
	argTypes := map[string]ir.TypeRef{}
	for _, arg := range op.Arguments {
		argTypes[arg.Name] = arg.TypeRef
	}
	var out []procedureArg
	add := func(name, field, rustType string, ref ir.TypeRef) error {
		optional := strings.HasPrefix(rustType, "Option<")
		s, err := b.fieldShape(ref, optional)
		if err != nil {
			return err
		}
		recordField := recordFieldName(field)
		out = append(out, procedureArg{Field: field, RecordField: recordField, Name: name, Type: s.rustType, Optional: optional, Put: s.put})
		return nil
	}
	params := append(append(append([]registry.RustParam{}, endpoint.PathArgs...), endpoint.QueryArgs...), endpoint.BodyArgs...)
	if input := endpoint.Input; input != nil {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(input.RustType, "Option<"), "types::"), ">")
		if err := add("input", "input", input.RustType, ir.TypeRef{Name: name}); err != nil {
			return nil, err
		}
	}
	for _, param := range params {
		ref, ok := argTypes[param.Name]
		if !ok {
			return nil, fmt.Errorf("argument %s is not among the operation's", param.Name)
		}
		if err := add(param.Name, param.Field, param.RustType, ref); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// recordFieldName is a record's field for the Args field field: the same,
// unless a record reserves the name, which takes a trailing underscore.
func recordFieldName(field string) string {
	if reservedFields[strings.TrimPrefix(field, "r#")] {
		return strings.TrimPrefix(field, "r#") + "_"
	}
	return field
}

// operation is the service's operation endpoint serves: the one of its
// name, and, when two sets share the name, of its method and path.
func (s schemaSet) operation(endpoint registry.RustEndpoint) (*ir.FieldDef, error) {
	var named []*ir.FieldDef
	for _, set := range s[0].OperationSets {
		for _, op := range set.Operations {
			if op.Name == endpoint.Name {
				named = append(named, op)
			}
		}
	}
	if len(named) == 1 {
		return named[0], nil
	}
	for _, op := range named {
		path := "/" + strings.Trim(op.RestPath, "/")
		if strings.EqualFold(op.HTTPMethod, endpoint.Method) && op.RestPath != "" && strings.HasSuffix(endpoint.Path, path) {
			return op, nil
		}
	}
	return nil, fmt.Errorf("topcoat: no operation of %s serves %s.%s (%s %s)", s[0].Name, endpoint.Namespace, endpoint.Name, strings.ToUpper(endpoint.Method), endpoint.Path)
}
