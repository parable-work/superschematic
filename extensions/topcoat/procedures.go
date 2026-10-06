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
	// Output is the result's record type, "()" without a result, and
	// ReadOutput the expression that reads it from the result's JSON
	// (`json`).
	Output     string
	ReadOutput string
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

// proceduresOf is a procedure per mounted operation of api, its argument
// and result types added to records.
func proceduresOf(records *recordBuilder, api *registry.RustAPI, service string) ([]procedure, error) {
	var out []procedure
	for _, endpoint := range api.Endpoints {
		op, err := records.operation(endpoint)
		if err != nil {
			return nil, err
		}
		p := procedure{
			Namespace: endpoint.Namespace,
			Name:      endpoint.Name,
			Route:     "/_superschematic/" + service + "/" + kebabCase(endpoint.Namespace) + "/" + kebabCase(endpoint.Name),
			SnakeName: endpoint.SnakeName(),
			Output:    "()",
		}
		if endpoint.HasArgs() {
			p.ArgsName, p.ArgsRecord = endpoint.ArgsName, endpoint.HandlerName+"ArgsRecord"
			if p.Args, err = records.procedureArgs(endpoint, op); err != nil {
				return nil, fmt.Errorf("topcoat: procedure of %s.%s: %w", endpoint.Namespace, endpoint.Name, err)
			}
		}
		if endpoint.OutputRustType != "()" {
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
		recordField := field
		if reservedFields[strings.TrimPrefix(field, "r#")] {
			recordField = strings.TrimPrefix(field, "r#") + "_"
		}
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

// operation is the service's operation endpoint serves: the one of its
// name, and, when two sets share the name, of its method and path.
func (b *recordBuilder) operation(endpoint registry.RustEndpoint) (*ir.FieldDef, error) {
	var named []*ir.FieldDef
	for _, set := range b.schemaSet[0].OperationSets {
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
	return nil, fmt.Errorf("topcoat: no operation of %s serves %s.%s (%s %s)", b.schemaSet[0].Name, endpoint.Namespace, endpoint.Name, strings.ToUpper(endpoint.Method), endpoint.Path)
}
