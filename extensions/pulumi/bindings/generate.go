package bindings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/extensions/pulumi"
	"github.com/parable-work/superschematic/extensions/pulumi/internal/names"
	ir "github.com/parable-work/superschematic/ir"
)

// Environment is one environment a binding covers.
type Environment struct {
	// Resolved is the resolved environment, as environment.json holds it.
	Resolved *ir.ResolvedEnvironment

	// Outputs are what the environment's last apply exported, or nil for
	// an environment not applied yet. A parameterized environment has
	// none: each member is a run of its own, read over its stack
	// reference or its outputs file.
	Outputs *Outputs
}

// The files Generate returns, by path under the binding's directory.
const (
	// ValuesFile is the package of plain values.
	ValuesFile = "binding.go"

	// PulumiFile is the package a hand-written Pulumi program reads.
	PulumiFile = "pulumi/binding.go"
)

// Generate returns the two packages of the binding over envs, the
// environments of one stack:
//
//   - ValuesFile, package pkg, declares Environment with one field per
//     deployable, each holding the deployable's name and address in the
//     environment and a field per node it owns with the node's outputs,
//     and a value per environment with outputs (`Staging.ShopAPI.Address`);
//   - PulumiFile, package pkg+"pulumi", declares the same types over
//     Pulumi outputs, and a function per environment that reads it over a
//     stack reference to the environment's stack, taking a member's
//     parameter values as arguments.
//
// A node's fields are the outputs the program exports for it
// (pulumi.Exports), typed by the values the outputs files hold: a string,
// a bool, a float64, or any for anything else. An output no file holds
// yet is a string. Nodes owned by something other than a deployable, such
// as DNS records, sit in a field of their own; a node an edge owns sits
// with the edge's server.
func Generate(pkg string, envs []Environment) (map[string][]byte, error) {
	m, err := newModel(pkg, envs)
	if err != nil {
		return nil, err
	}
	values, err := gofmt(ValuesFile, m.values())
	if err != nil {
		return nil, err
	}
	refs, err := gofmt(PulumiFile, m.pulumi())
	if err != nil {
		return nil, err
	}
	return map[string][]byte{ValuesFile: values, PulumiFile: refs}, nil
}

// Write writes Generate's files under dir.
func Write(dir, pkg string, envs []Environment) error {
	files, err := Generate(pkg, envs)
	if err != nil {
		return err
	}
	for _, name := range []string{ValuesFile, PulumiFile} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

func gofmt(name string, src []byte) ([]byte, error) {
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("bindings: format %s: %w\n%s", name, err, src)
	}
	return out, nil
}

// kind is the Go type of an output.
type kind int

const (
	unseen kind = iota
	kindString
	kindBool
	kindNumber
	kindAny
)

func kindOf(v any) kind {
	switch v.(type) {
	case string:
		return kindString
	case bool:
		return kindBool
	case float64, json.Number:
		return kindNumber
	}
	return kindAny
}

func (k kind) merge(other kind) kind {
	switch {
	case k == unseen:
		return other
	case other == unseen, k == other:
		return k
	}
	return kindAny
}

// output is one field of a node's type.
type output struct {
	name  string // the output's name
	key   string // the stack output's name
	field string
	kind  kind
}

// node is one node's type.
type node struct {
	id, token, typ string
	outputs        []*output
	byName         map[string]*output
}

// member is a node in a group.
type member struct {
	field string
	node  *node
}

// group is one field of Environment: a deployable, or another owner of
// nodes.
type group struct {
	owner      string
	field      string
	kind       ir.DeployableKind // empty for an owner that is not a deployable
	hasAddress bool
	members    []*member
	byID       map[string]*member
}

type model struct {
	pkg, stack, project string
	envs                []Environment
	groups              []*group
	byOwner             map[string]*group
	nodes               map[string]*node

	// needsBool is whether the Pulumi package needs its bool helper.
	needsBool bool
}

var pkgPattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

func newModel(pkg string, envs []Environment) (*model, error) {
	if !pkgPattern.MatchString(pkg) {
		return nil, fmt.Errorf("bindings: package name %q must be lowercase letters and digits", pkg)
	}
	if len(envs) == 0 {
		return nil, fmt.Errorf("bindings: no environments")
	}
	m := &model{pkg: pkg, envs: envs, byOwner: map[string]*group{}, nodes: map[string]*node{}}
	seen := map[string]bool{}
	for _, env := range envs {
		res := env.Resolved
		if res == nil {
			return nil, fmt.Errorf("bindings: an environment has no resolved environment")
		}
		if m.stack == "" {
			m.stack = res.Stack
		}
		if res.Stack != m.stack {
			return nil, fmt.Errorf("bindings: environments of stacks %s and %s; a binding covers one stack", m.stack, res.Stack)
		}
		if seen[res.Environment] {
			return nil, fmt.Errorf("bindings: environment %s appears twice", res.Environment)
		}
		seen[res.Environment] = true
		if out := env.Outputs; out != nil {
			if len(res.Parameters) > 0 {
				return nil, fmt.Errorf("bindings: environment %s is parameterized, so it has no value of its own; leave its outputs out", res.Environment)
			}
			if out.Stack != res.Stack || out.Environment != res.Environment {
				return nil, fmt.Errorf("bindings: the outputs of %s/%s were given for environment %s/%s", out.Stack, out.Environment, res.Stack, res.Environment)
			}
		}
	}
	var err error
	if m.project, err = pulumi.ProjectName(m.stack); err != nil {
		return nil, fmt.Errorf("bindings: %w", err)
	}
	for _, env := range envs {
		if err := m.add(env); err != nil {
			return nil, err
		}
	}
	return m, m.finish()
}

// add takes one environment's deployables, nodes, exports and observed
// output values into the model.
func (m *model) add(env Environment) error {
	res := env.Resolved
	exports, err := pulumi.Exports(res)
	if err != nil {
		return fmt.Errorf("bindings: environment %s: %w", res.Environment, err)
	}
	deployables := map[string]*ir.ResolvedDeployable{}
	for _, d := range res.Deployables {
		deployables[d.Name] = d
		g := m.group(d.Name)
		g.kind = d.Kind
		if d.Address != nil {
			g.hasAddress = true
		}
	}
	edgeFrom := map[string]string{}
	for _, e := range res.Edges {
		edgeFrom[e.ID] = e.From
	}
	for _, r := range res.Resources.Resources {
		n := m.nodes[r.ID]
		if n == nil {
			n = &node{id: r.ID, token: r.Type, typ: names.Exported(r.ID), byName: map[string]*output{}}
			m.nodes[r.ID] = n
		}
		owners := r.Owners
		if len(owners) == 0 {
			owners = []string{""}
		}
		for _, owner := range owners {
			if from, ok := edgeFrom[owner]; ok {
				owner = from
			}
			g := m.group(owner)
			if g.byID[r.ID] == nil {
				g.byID[r.ID] = &member{field: memberField(owner, r.ID), node: n}
			}
		}
	}
	for _, exp := range exports {
		n := m.nodes[exp.Resource]
		if n.byName[exp.Name] == nil {
			n.byName[exp.Name] = &output{name: exp.Name, key: exp.Key, field: names.Exported(exp.Name)}
		}
	}
	if env.Outputs != nil {
		for id, values := range env.Outputs.Resources {
			n := m.nodes[id]
			if n == nil {
				continue
			}
			for name, v := range values {
				if out := n.byName[name]; out != nil && v != nil {
					out.kind = out.kind.merge(kindOf(v))
				}
			}
		}
	}
	return nil
}

func (m *model) group(owner string) *group {
	g := m.byOwner[owner]
	if g == nil {
		field := names.Exported(owner)
		if owner == "" {
			field = "Resources"
		}
		g = &group{owner: owner, field: field, byID: map[string]*member{}}
		m.byOwner[owner] = g
	}
	return g
}

// memberField names a node in its owner's group: the node's ID without
// the owner's name in front (shop-api.service is Service in ShopAPI).
func memberField(owner, id string) string {
	if rest, ok := strings.CutPrefix(id, owner+"."); ok && owner != "" && rest != "" {
		return names.Exported(rest)
	}
	return names.Exported(id)
}

// finish sorts the model and refuses names that collide.
func (m *model) finish() error {
	types := map[string]string{"Environment": "the environment type"}
	claim := func(typ, what string) error {
		if other, ok := types[typ]; ok {
			return fmt.Errorf("bindings: %s and %s are both type %s", other, what, typ)
		}
		types[typ] = what
		return nil
	}
	fields := map[string]string{}
	for _, owner := range sortedKeys(m.byOwner) {
		g := m.byOwner[owner]
		if other, ok := fields[g.field]; ok {
			return fmt.Errorf("bindings: %s and %s are both field %s of Environment", other, g.describe(), g.field)
		}
		fields[g.field] = g.describe()
		if err := claim(g.field, g.describe()); err != nil {
			return err
		}
		inGroup := map[string]string{}
		if g.kind != "" {
			inGroup["Name"] = "its name"
			if g.hasAddress {
				inGroup["Address"] = "its address"
			}
		}
		for _, id := range sortedKeys(g.byID) {
			mem := g.byID[id]
			if other, ok := inGroup[mem.field]; ok {
				return fmt.Errorf("bindings: in %s, node %s and %s are both field %s", g.describe(), id, other, mem.field)
			}
			inGroup[mem.field] = "node " + id
			g.members = append(g.members, mem)
		}
		sort.Slice(g.members, func(i, j int) bool { return g.members[i].field < g.members[j].field })
		m.groups = append(m.groups, g)
	}
	sort.Slice(m.groups, func(i, j int) bool { return m.groups[i].field < m.groups[j].field })
	for _, id := range sortedKeys(m.nodes) {
		n := m.nodes[id]
		if err := claim(n.typ, "node "+id); err != nil {
			return err
		}
		inNode := map[string]string{}
		for _, name := range sortedKeys(n.byName) {
			out := n.byName[name]
			if other, ok := inNode[out.field]; ok {
				return fmt.Errorf("bindings: outputs %s and %s of node %s are both field %s", other, name, id, out.field)
			}
			inNode[out.field] = name
			if out.kind == unseen {
				out.kind = kindString
			}
			if out.kind == kindBool {
				m.needsBool = true
			}
			n.outputs = append(n.outputs, out)
		}
		sort.Slice(n.outputs, func(i, j int) bool { return n.outputs[i].field < n.outputs[j].field })
	}
	funcs := map[string]string{}
	for _, env := range m.envs {
		fn := names.Exported(env.Resolved.Environment)
		if other, ok := types[fn]; ok {
			return fmt.Errorf("bindings: environment %s and %s are both %s", env.Resolved.Environment, other, fn)
		}
		funcs[fn] = env.Resolved.Environment
		params := map[string]string{}
		for _, param := range env.Resolved.Parameters {
			ident := names.Unexported(param)
			if other, ok := params[ident]; ok {
				return fmt.Errorf("bindings: parameters %s and %s of environment %s are both argument %s", other, param, env.Resolved.Environment, ident)
			}
			params[ident] = param
		}
	}
	if len(funcs) != len(m.envs) {
		return fmt.Errorf("bindings: two environments share a Go name")
	}
	if _, ok := funcs["Environments"]; ok {
		return fmt.Errorf("bindings: environment Environments takes the name of the map of environments")
	}
	return nil
}

func (g *group) describe() string {
	switch {
	case g.kind != "":
		return string(g.kind) + " " + g.owner
	case g.owner == "dns":
		return "the nodes of the DNS records"
	case g.owner == "":
		return "the nodes no one owns"
	}
	return "the nodes " + g.owner + " owns"
}

func (g *group) comment() string {
	switch {
	case g.kind != "":
		return fmt.Sprintf("%s is %s: its name and address in an environment, and the outputs of the nodes it owns.", g.field, g.describe())
	case g.owner == "dns":
		return fmt.Sprintf("%s holds the outputs of the nodes of an environment's DNS records.", g.field)
	}
	return fmt.Sprintf("%s holds the outputs of %s.", g.field, g.describe())
}

func (k kind) goType() string {
	switch k {
	case kindBool:
		return "bool"
	case kindNumber:
		return "float64"
	case kindAny:
		return "any"
	}
	return "string"
}

func (k kind) pulumiType() string {
	switch k {
	case kindBool:
		return "pulumi.BoolOutput"
	case kindNumber:
		return "pulumi.Float64Output"
	case kindAny:
		return "pulumi.AnyOutput"
	}
	return "pulumi.StringOutput"
}

// header writes the generated-code header and the package clause.
func (m *model) header(b *bytes.Buffer, pkg, doc string) {
	fmt.Fprintf(b, "// Code generated by superschematic from the outputs of stack %s. DO NOT EDIT.\n\n", m.stack)
	writeComment(b, "", doc)
	fmt.Fprintf(b, "package %s\n\n", pkg)
}

// types writes Environment, the group types and the node types, with the
// field types fieldType gives.
func (m *model) types(b *bytes.Buffer, scalar string, fieldType func(kind) string) {
	writeComment(b, "", fmt.Sprintf("Environment is one environment of stack %s.", m.stack))
	b.WriteString("type Environment struct {\n")
	for _, g := range m.groups {
		if g.kind != "" {
			writeComment(b, "\t", fmt.Sprintf("%s is %s.", g.field, g.describe()))
		} else {
			writeComment(b, "\t", fmt.Sprintf("%s holds the outputs of %s.", g.field, g.describe()))
		}
		fmt.Fprintf(b, "\t%s %s\n", g.field, g.field)
	}
	b.WriteString("}\n\n")
	for _, g := range m.groups {
		writeComment(b, "", g.comment())
		fmt.Fprintf(b, "type %s struct {\n", g.field)
		if g.kind != "" {
			writeComment(b, "\t", fmt.Sprintf("Name is the %s's name in the environment.", g.kind))
			fmt.Fprintf(b, "\tName %s\n", scalar)
			if g.hasAddress {
				writeComment(b, "\t", fmt.Sprintf("Address is how an edge reaches the %s.", g.kind))
				fmt.Fprintf(b, "\tAddress %s\n", scalar)
			}
		}
		for _, mem := range g.members {
			writeComment(b, "\t", fmt.Sprintf("%s is node %s, a %s.", mem.field, mem.node.id, mem.node.token))
			fmt.Fprintf(b, "\t%s %s\n", mem.field, mem.node.typ)
		}
		b.WriteString("}\n\n")
	}
	for _, id := range sortedKeys(m.nodes) {
		n := m.nodes[id]
		writeComment(b, "", fmt.Sprintf("%s holds the outputs of node %s.", n.typ, n.id))
		fmt.Fprintf(b, "type %s struct {\n", n.typ)
		for _, out := range n.outputs {
			writeComment(b, "\t", fmt.Sprintf("%s is the node's %s output.", out.field, out.name))
			fmt.Fprintf(b, "\t%s %s\n", out.field, fieldType(out.kind))
		}
		b.WriteString("}\n\n")
	}
}

// values returns the source of the package of plain values.
func (m *model) values() []byte {
	var b bytes.Buffer
	m.header(&b, m.pkg, fmt.Sprintf(
		"Package %s is the typed binding over the outputs of stack %s: one value per applied environment, with one field per deployable. Generate it again after an apply.",
		m.pkg, m.stack))
	m.types(&b, "string", kind.goType)
	var applied []string
	for _, env := range m.envs {
		if env.Outputs == nil {
			continue
		}
		name := names.Exported(env.Resolved.Environment)
		applied = append(applied, name)
		writeComment(&b, "", fmt.Sprintf("%s is environment %s, as its last apply left it.", name, env.Resolved.Environment))
		fmt.Fprintf(&b, "var %s = Environment{\n", name)
		m.environmentValue(&b, env)
		b.WriteString("}\n\n")
	}
	writeComment(&b, "", "Environments are the environments with a value, by name.")
	b.WriteString("var Environments = map[string]*Environment{\n")
	sort.Strings(applied)
	for _, name := range applied {
		for _, env := range m.envs {
			if names.Exported(env.Resolved.Environment) == name {
				fmt.Fprintf(&b, "\t%q: &%s,\n", env.Resolved.Environment, name)
			}
		}
	}
	b.WriteString("}\n")
	return b.Bytes()
}

func (m *model) environmentValue(b *bytes.Buffer, env Environment) {
	res := env.Resolved
	values := env.Outputs.Resources
	for _, g := range m.groups {
		var body bytes.Buffer
		if d := res.Deployable(g.owner); d != nil && g.kind != "" {
			if name, ok := resolve(d.ResourceName, values, env.Outputs.Parameters); ok {
				fmt.Fprintf(&body, "\t\tName: %s,\n", strconv.Quote(name))
			}
			if address, ok := resolve(d.Address, values, env.Outputs.Parameters); ok && d.Address != nil {
				fmt.Fprintf(&body, "\t\tAddress: %s,\n", strconv.Quote(address))
			}
		}
		for _, mem := range g.members {
			var fields bytes.Buffer
			for _, out := range mem.node.outputs {
				v, ok := values[mem.node.id][out.name]
				if !ok || v == nil {
					continue
				}
				fmt.Fprintf(&fields, "\t\t\t%s: %s,\n", out.field, literal(v, out.kind))
			}
			if fields.Len() > 0 {
				fmt.Fprintf(&body, "\t\t%s: %s{\n%s\t\t},\n", mem.field, mem.node.typ, fields.String())
			}
		}
		if body.Len() > 0 {
			fmt.Fprintf(b, "\t%s: %s{\n%s\t},\n", g.field, g.field, body.String())
		}
	}
}

// resolve reads a name or an address as a string: references take their
// values from the outputs and the parameters. It reports false when a
// referenced output has no value.
func resolve(v any, outputs map[string]map[string]any, params map[string]string) (string, bool) {
	switch v := v.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case ir.Parameter:
		value, ok := params[string(v)]
		return value, ok
	case ir.Output:
		value, ok := outputs[v.Resource][v.Name]
		if !ok || value == nil {
			return "", false
		}
		if s, ok := value.(string); ok {
			return s, true
		}
		data, err := json.Marshal(value)
		return string(data), err == nil
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			s, ok := resolve(part, outputs, params)
			if !ok {
				return "", false
			}
			b.WriteString(s)
		}
		return b.String(), true
	}
	data, err := json.Marshal(v)
	return string(data), err == nil
}

// literal writes an output's value as a Go expression of its field's
// type.
func literal(v any, k kind) string {
	if k == kindNumber {
		if f, ok := v.(float64); ok {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	}
	return anyLiteral(v)
}

func anyLiteral(v any) string {
	switch v := v.(type) {
	case nil:
		return "nil"
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return "float64(" + strconv.FormatFloat(v, 'g', -1, 64) + ")"
	case map[string]any:
		var parts []string
		for _, key := range sortedKeys(v) {
			parts = append(parts, strconv.Quote(key)+": "+anyLiteral(v[key]))
		}
		return "map[string]any{" + strings.Join(parts, ", ") + "}"
	case []any:
		var parts []string
		for _, inner := range v {
			parts = append(parts, anyLiteral(inner))
		}
		return "[]any{" + strings.Join(parts, ", ") + "}"
	}
	data, _ := json.Marshal(v)
	return strconv.Quote(string(data))
}

// pulumi returns the source of the package a hand-written Pulumi program
// reads.
func (m *model) pulumi() []byte {
	var b bytes.Buffer
	pkg := m.pkg + "pulumi"
	m.header(&b, pkg, fmt.Sprintf(
		"Package %s reads the outputs of stack %s in a hand-written Pulumi program, over a stack reference to an environment's stack. A program reads each environment once: its reference is a resource.",
		pkg, m.stack))
	if m.needsBool {
		b.WriteString("import (\n\t\"fmt\"\n\n\t\"github.com/pulumi/pulumi/sdk/v3/go/pulumi\"\n)\n\n")
	} else {
		b.WriteString("import \"github.com/pulumi/pulumi/sdk/v3/go/pulumi\"\n\n")
	}
	writeComment(&b, "", fmt.Sprintf("Project is the Pulumi project of stack %s, which holds a stack per run of each of its environments.", m.stack))
	fmt.Fprintf(&b, "const Project = %q\n\n", m.project)
	m.types(&b, "pulumi.StringOutput", kind.pulumiType)
	for _, env := range m.envs {
		m.reader(&b, env.Resolved)
	}
	for _, id := range sortedKeys(m.nodes) {
		n := m.nodes[id]
		fmt.Fprintf(&b, "func read%s(ref *pulumi.StackReference) %s {\n\treturn %s{\n", n.typ, n.typ, n.typ)
		for _, out := range n.outputs {
			fmt.Fprintf(&b, "\t\t%s: %s,\n", out.field, readOutput(out))
		}
		b.WriteString("\t}\n}\n\n")
	}
	if m.needsBool {
		b.WriteString(`// boolOutput reads a boolean output over ref.
func boolOutput(ref *pulumi.StackReference, key string) pulumi.BoolOutput {
	return ref.GetOutput(pulumi.String(key)).ApplyT(func(v any) (bool, error) {
		b, ok := v.(bool)
		if !ok {
			return false, fmt.Errorf("stack reference output %q is %T, not a bool", key, v)
		}
		return b, nil
	}).(pulumi.BoolOutput)
}
`)
	}
	return b.Bytes()
}

func readOutput(out *output) string {
	key := "pulumi.String(" + strconv.Quote(out.key) + ")"
	switch out.kind {
	case kindBool:
		return "boolOutput(ref, " + strconv.Quote(out.key) + ")"
	case kindNumber:
		return "ref.GetFloat64Output(" + key + ")"
	case kindAny:
		return "ref.GetOutput(" + key + ")"
	}
	return "ref.GetStringOutput(" + key + ")"
}

// reader writes the function that reads one environment over a stack
// reference.
func (m *model) reader(b *bytes.Buffer, env *ir.ResolvedEnvironment) {
	fn := names.Exported(env.Environment)
	base, _ := pulumi.StackName(env.Environment, nil, nil)
	idents := map[string]string{}
	var args []string
	literal := "/" + m.project + "/" + base
	var stackExpr []string
	described := base
	for _, param := range env.Parameters {
		ident := names.Unexported(param)
		idents[param] = ident
		args = append(args, ident+" string")
		prefix := "." + names.Kebab(param) + "-"
		stackExpr = append(stackExpr, strconv.Quote(literal+prefix), ident)
		literal = ""
		described += prefix + "<" + ident + ">"
	}
	if literal != "" {
		stackExpr = append(stackExpr, strconv.Quote(literal))
	}
	doc := fmt.Sprintf("%s reads environment %s over a stack reference to its stack, %s.", fn, env.Environment, described)
	if len(env.Parameters) > 0 {
		doc = fmt.Sprintf("%s reads the member of environment %s that the parameter values name, over a stack reference to its stack, %s.", fn, env.Environment, described)
	}
	writeComment(b, "", doc+" A field the environment lacks reads as an error when used.")
	fmt.Fprintf(b, "func %s(ctx *pulumi.Context, %sopts ...pulumi.ResourceOption) (*Environment, error) {\n", fn, joinArgs(args))
	fmt.Fprintf(b, "\tref, err := pulumi.NewStackReference(ctx, ctx.Organization()+%s, nil, opts...)\n", strings.Join(stackExpr, "+"))
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn &Environment{\n")
	present := map[string]bool{}
	for _, r := range env.Resources.Resources {
		present[r.ID] = true
	}
	for _, g := range m.groups {
		var body bytes.Buffer
		d := env.Deployable(g.owner)
		if d != nil && g.kind != "" {
			fmt.Fprintf(&body, "\t\t\tName: %s,\n", m.outputExpr(d.ResourceName, idents))
			if g.hasAddress && d.Address != nil {
				fmt.Fprintf(&body, "\t\t\tAddress: %s,\n", m.outputExpr(d.Address, idents))
			}
		}
		inEnv := d != nil
		for _, mem := range g.members {
			inEnv = inEnv || present[mem.node.id]
		}
		if !inEnv {
			continue
		}
		for _, mem := range g.members {
			fmt.Fprintf(&body, "\t\t\t%s: read%s(ref),\n", mem.field, mem.node.typ)
		}
		fmt.Fprintf(b, "\t\t%s: %s{\n%s\t\t},\n", g.field, g.field, body.String())
	}
	b.WriteString("\t}, nil\n}\n\n")
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.Join(args, ", ") + ", "
}

// outputExpr writes a name or an address as a pulumi.StringOutput:
// references read the stack reference and the parameter arguments.
func (m *model) outputExpr(v any, idents map[string]string) string {
	switch v := v.(type) {
	case string:
		return "pulumi.String(" + strconv.Quote(v) + ").ToStringOutput()"
	case ir.Parameter:
		return "pulumi.String(" + idents[string(v)] + ").ToStringOutput()"
	case ir.Output:
		return "ref.GetStringOutput(pulumi.String(" + strconv.Quote(m.key(v)) + "))"
	case ir.Concat:
		var format strings.Builder
		var args []string
		for _, part := range v {
			switch part := part.(type) {
			case string:
				format.WriteString(strings.ReplaceAll(part, "%", "%%"))
			case ir.Parameter:
				format.WriteString("%s")
				args = append(args, idents[string(part)])
			case ir.Output:
				format.WriteString("%v")
				args = append(args, "ref.GetOutput(pulumi.String("+strconv.Quote(m.key(part))+"))")
			}
		}
		if len(args) == 0 {
			return "pulumi.String(" + strconv.Quote(format.String()) + ").ToStringOutput()"
		}
		return "pulumi.Sprintf(" + strconv.Quote(format.String()) + ", " + strings.Join(args, ", ") + ")"
	}
	data, _ := json.Marshal(v)
	return "pulumi.String(" + strconv.Quote(string(data)) + ").ToStringOutput()"
}

// key is the stack output an output reference reads.
func (m *model) key(out ir.Output) string {
	if n := m.nodes[out.Resource]; n != nil {
		if o := n.byName[out.Name]; o != nil {
			return o.key
		}
	}
	return out.Resource + "." + out.Name
}

// writeComment writes text as a line comment wrapped at 76 columns.
func writeComment(b *bytes.Buffer, indent, text string) {
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(indent)+3+len(line)+1+len(word) > 79 {
			fmt.Fprintf(b, "%s// %s\n", indent, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	fmt.Fprintf(b, "%s// %s\n", indent, line)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
