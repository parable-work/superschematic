package pulumi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	ir "github.com/parable-work/superschematic/ir"
)

// ProgramFile is the file Render writes: the Pulumi YAML project.
const ProgramFile = "Pulumi.yaml"

// parentKey is the program's resource key for the stack reference to the
// parent environment's stack, which owns the nodes a member of a
// parameterized environment inherits (section 5.4).
const parentKey = "parent-stack"

// Render writes the program for env's resource graph to dir/Pulumi.yaml
// (section 6.5):
//
//   - the project is the stack's (ProjectName), and each parameter is a
//     string of the project's config, which the run sets;
//   - each node is one resource, keyed by its ID with every character
//     other than a letter, a digit, `_` and `-` turned into `-`. Its
//     `name` is the ID, so state is keyed on the ID;
//   - a property that references another node's output reads
//     `${key.output}`, and one that references a parameter reads
//     `${parameter}`;
//   - `options.dependsOn` lists the node's dependencies, and
//     `options.version` the plugin version ProviderVersions pins for its
//     package;
//   - an inherited node is not a resource. The program reads its outputs
//     from the parent environment's stack through a stack reference;
//   - the outputs are Exports(env): every node's `id` and every output the
//     environment references.
//
// Render reads nothing but env and writes nothing but Pulumi.yaml: the
// same environment renders the same bytes, and a parameter's value never
// reaches the file. It leaves the stack settings files the CLI keeps beside
// it (Pulumi.<stack>.yaml) alone.
func (p *Provisioner) Render(env *ir.ResolvedEnvironment, dir string) error {
	data, err := renderProgram(env, p.ProviderVersions)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ProgramFile), data, 0o644)
}

// Export is one stack output of a rendered program: one output of one
// node.
type Export struct {
	// Key is the stack output's name: the node's ID, a dot and the
	// output's name (`shop-api.service.uri`).
	Key string

	// Resource is the node's ID.
	Resource string

	// Name is the output's name. It may be a property path:
	// `dnsResourceRecords[0].data`.
	Name string
}

// Exports returns the stack outputs the program Render writes for env,
// sorted by key: every node's `id`, and every output env references in a
// node's properties, a deployable's name or address, a binding's value or
// a DNS record. An inherited node's outputs are read from the parent
// environment's stack and exported again, so a member's outputs cover its
// whole graph.
func Exports(env *ir.ResolvedEnvironment) ([]Export, error) {
	prog, err := newProgram(env, nil)
	if err != nil {
		return nil, err
	}
	return prog.exports, nil
}

// program is one environment's graph checked and keyed for rendering.
type program struct {
	env       *ir.ResolvedEnvironment
	project   string
	versions  map[string]string
	keys      map[string]string // node ID -> resource key
	inherited map[string]bool
	params    map[string]bool
	exports   []Export

	// parent is the parent environment's stack, which a program with
	// inherited nodes references; empty without them.
	parent string
}

func newProgram(env *ir.ResolvedEnvironment, versions map[string]string) (*program, error) {
	if env == nil || env.Resources == nil {
		return nil, fmt.Errorf("pulumi: no resource graph to render")
	}
	project, err := ProjectName(env.Stack)
	if err != nil {
		return nil, fmt.Errorf("pulumi: %w", err)
	}
	prog := &program{
		env:       env,
		project:   project,
		versions:  versions,
		keys:      map[string]string{},
		inherited: map[string]bool{},
		params:    map[string]bool{},
	}
	taken := map[string]string{parentKey: "the parent stack reference", "pulumi": "Pulumi's built-in variable"}
	for _, param := range env.Parameters {
		prog.params[param] = true
		taken[param] = "parameter " + param
	}
	for _, res := range env.Resources.Resources {
		if err := checkID(res.ID); err != nil {
			return nil, err
		}
		if !tokenPattern.MatchString(res.Type) {
			return nil, fmt.Errorf("pulumi: node %s has type %q, which is not a Pulumi type token (package:module:Type)", res.ID, res.Type)
		}
		if _, dup := prog.keys[res.ID]; dup {
			return nil, fmt.Errorf("pulumi: the graph has two nodes %s", res.ID)
		}
		key := resourceKey(res.ID)
		if other, ok := taken[key]; ok {
			return nil, fmt.Errorf("pulumi: node %s takes the program key %s, which %s has", res.ID, key, other)
		}
		taken[key] = "node " + res.ID
		prog.keys[res.ID] = key
		if res.Inherited {
			prog.inherited[res.ID] = true
		}
	}
	if len(prog.inherited) > 0 {
		if env.Extends == "" {
			return nil, fmt.Errorf("pulumi: environment %s inherits nodes but extends no environment", env.Environment)
		}
		// A member's parent is named without parameter values: the parent
		// of a parameterized environment is a plain one (section 5.4).
		if prog.parent, err = StackName(env.Extends, nil, nil); err != nil {
			return nil, fmt.Errorf("pulumi: %w", err)
		}
	}
	if err := prog.collectExports(); err != nil {
		return nil, err
	}
	return prog, nil
}

// collectExports gathers every node's `id` and every output env
// references, and checks each reference.
func (p *program) collectExports() error {
	seen := map[ir.Output]bool{}
	add := func(where string, v any) error {
		outputs, params := ir.ValueRefs(v)
		for _, param := range params {
			if !p.params[param] {
				return fmt.Errorf("pulumi: %s references parameter %s, which environment %s does not declare", where, param, p.env.Environment)
			}
		}
		for _, out := range outputs {
			if _, ok := p.keys[out.Resource]; !ok {
				return fmt.Errorf("pulumi: %s references output %s of node %s, which the graph lacks", where, out.Name, out.Resource)
			}
			if !outputPattern.MatchString(out.Name) {
				return fmt.Errorf("pulumi: %s references output %q of node %s; an output is a property name or path (`uri`, `records[0].data`)", where, out.Name, out.Resource)
			}
			seen[out] = true
		}
		return nil
	}
	for _, res := range p.env.Resources.Resources {
		seen[ir.Output{Resource: res.ID, Name: "id"}] = true
		if res.Inherited {
			continue
		}
		if err := add("node "+res.ID, map[string]any(res.Properties)); err != nil {
			return err
		}
		for _, dep := range res.DependsOn {
			if _, ok := p.keys[dep]; !ok {
				return fmt.Errorf("pulumi: node %s depends on node %s, which the graph lacks", res.ID, dep)
			}
		}
	}
	for _, d := range p.env.Deployables {
		if err := add("deployable "+d.Name, []any{d.ResourceName, d.Address}); err != nil {
			return err
		}
		for _, b := range d.Bindings {
			if err := add("binding "+d.Name+"."+b.Field, b.Value); err != nil {
				return err
			}
		}
	}
	if p.env.DNS != nil {
		for _, rec := range p.env.DNS.Records {
			if err := add("DNS record of "+rec.Deployable, []any{rec.Name, rec.Value}); err != nil {
				return err
			}
		}
	}
	byKey := map[string]ir.Output{}
	for out := range seen {
		key := exportKey(out)
		if other, ok := byKey[key]; ok && other != out {
			return fmt.Errorf("pulumi: outputs %s of %s and %s of %s both export as %s", other.Name, other.Resource, out.Name, out.Resource, key)
		}
		byKey[key] = out
		p.exports = append(p.exports, Export{Key: key, Resource: out.Resource, Name: out.Name})
	}
	sort.Slice(p.exports, func(i, j int) bool { return p.exports[i].Key < p.exports[j].Key })
	return nil
}

func exportKey(out ir.Output) string { return out.Resource + "." + out.Name }

var (
	// tokenPattern is a Pulumi type token: package, module and type.
	tokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*:[^:\s]*:[^:\s]+$`)

	// outputPattern is an output name or a property path into one.
	outputPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*|\[[0-9]+\])*$`)
)

// checkID refuses an ID the program cannot carry: an empty one, one with a
// character that is special in an interpolation or a URN target pattern,
// and one with `::`, which separates a URN's parts.
func checkID(id string) error {
	if id == "" {
		return fmt.Errorf("pulumi: the graph has a node with no ID")
	}
	if strings.Contains(id, "::") || strings.ContainsAny(id, "*\"$\\{}[]") || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("pulumi: node ID %q has a space, a control character, `::` or one of * \" $ \\ { } [ ]", id)
	}
	return nil
}

// resourceKey is a node's key in the program: its ID with every character
// other than a letter, a digit, `_` and `-` turned into `-`, and `_` in
// front when it would not begin with a letter or `_`.
func resourceKey(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	key := b.String()
	if c := key[0]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' {
		key = "_" + key
	}
	return key
}

// renderProgram returns the bytes of Pulumi.yaml for env.
func renderProgram(env *ir.ResolvedEnvironment, versions map[string]string) ([]byte, error) {
	prog, err := newProgram(env, versions)
	if err != nil {
		return nil, err
	}
	root := mapping()
	addPair(root, "name", str(prog.project))
	addPair(root, "runtime", str("yaml"))
	addPair(root, "description", str(fmt.Sprintf("Environment %s of stack %s", env.Environment, env.Stack)))
	if len(env.Parameters) > 0 {
		config := mapping()
		for _, param := range env.Parameters {
			addPair(config, param, mapping(pair("type", str("string"))...))
		}
		addPair(root, "config", config)
	}
	resources := mapping()
	if prog.parent != "" {
		var inherited []string
		for _, res := range env.Resources.Resources {
			if res.Inherited {
				inherited = append(inherited, res.ID)
			}
		}
		ref := mapping(
			pair("type", str("pulumi:pulumi:StackReference"))...,
		)
		addPair(ref, "properties", mapping(pair("name", str(escape(prog.parent)))...))
		addPair(resources, parentKey, ref)
		resources.Content[len(resources.Content)-2].HeadComment = wrap(fmt.Sprintf(
			"The stack of environment %s, which owns the nodes this environment inherits: %s.",
			env.Extends, strings.Join(inherited, ", ")))
	}
	for _, res := range env.Resources.Resources {
		if res.Inherited {
			continue
		}
		node, err := prog.resource(res)
		if err != nil {
			return nil, err
		}
		addPair(resources, prog.keys[res.ID], node)
		comment := "phase " + string(res.Phase)
		if res.Phase == "" {
			comment = "no phase"
		}
		if len(res.Owners) > 0 {
			comment += "; owners " + strings.Join(res.Owners, ", ")
		}
		resources.Content[len(resources.Content)-2].HeadComment = comment
	}
	if len(resources.Content) > 0 {
		addPair(root, "resources", resources)
	}
	outputs := mapping()
	for _, exp := range prog.exports {
		addPair(outputs, exp.Key, str(prog.ref(ir.Output{Resource: exp.Resource, Name: exp.Name})))
	}
	if len(outputs.Content) > 0 {
		addPair(root, "outputs", outputs)
	}
	doc := &yaml.Node{
		Kind: yaml.DocumentNode,
		HeadComment: wrap(fmt.Sprintf(
			"Rendered by superschematic from the resource graph of environment %s of stack %s. "+
				"Do not edit: change the stack and render it again.", env.Environment, env.Stack)),
		Content: []*yaml.Node{root},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("pulumi: encode %s: %w", ProgramFile, err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resource renders one node.
func (p *program) resource(res *ir.Resource) (*yaml.Node, error) {
	node := mapping()
	addPair(node, "name", str(escape(res.ID)))
	addPair(node, "type", str(res.Type))
	if len(res.Properties) > 0 {
		props, err := p.value(map[string]any(res.Properties))
		if err != nil {
			return nil, fmt.Errorf("pulumi: node %s: %w", res.ID, err)
		}
		addPair(node, "properties", props)
	}
	options := mapping()
	var deps []string
	for _, dep := range res.DependsOn {
		ref := "${" + p.keys[dep] + "}"
		if p.inherited[dep] {
			ref = "${" + parentKey + "}"
		}
		if !slices.Contains(deps, ref) {
			deps = append(deps, ref)
		}
	}
	if len(deps) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, dep := range deps {
			list.Content = append(list.Content, str(dep))
		}
		addPair(options, "dependsOn", list)
	}
	if version := p.versions[res.Type[:strings.IndexByte(res.Type, ':')]]; version != "" {
		addPair(options, "version", str(version))
	}
	if len(options.Content) > 0 {
		addPair(node, "options", options)
	}
	return node, nil
}

// ref is the interpolation that reads an output: `${key.name}`, or for an
// inherited node the output the parent's stack exports.
func (p *program) ref(out ir.Output) string {
	if p.inherited[out.Resource] {
		return fmt.Sprintf(`${%s.outputs["%s"]}`, parentKey, exportKey(out))
	}
	return "${" + p.keys[out.Resource] + "." + out.Name + "}"
}

// value renders a property value. A string is escaped, so `$` stays
// literal; a reference becomes an interpolation; a concat becomes one
// string of both.
func (p *program) value(v any) (*yaml.Node, error) {
	switch v := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case string:
		return str(escape(v)), nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case float64:
		return number(v)
	case float32:
		return number(float64(v))
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return nil, err
		}
		return number(f)
	case ir.Output:
		return str(p.ref(v)), nil
	case ir.Parameter:
		return str("${" + string(v) + "}"), nil
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			switch part := part.(type) {
			case string:
				b.WriteString(escape(part))
			case ir.Output:
				b.WriteString(p.ref(part))
			case ir.Parameter:
				b.WriteString("${" + string(part) + "}")
			default:
				return nil, fmt.Errorf("a $concat part is %T, not a string or a reference", part)
			}
		}
		return str(b.String()), nil
	case map[string]any:
		if len(v) == 1 {
			for key := range v {
				if strings.HasPrefix(key, "fn::") {
					return nil, fmt.Errorf("an object whose one key is %q reads as a Pulumi YAML function call", key)
				}
			}
		}
		node := mapping()
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			inner, err := p.value(v[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			addPair(node, key, inner)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode}
		for i, inner := range v {
			item, err := p.value(inner)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			node.Content = append(node.Content, item)
		}
		return node, nil
	}
	// Any other Go value is read through its JSON form, as resolution
	// reads what a platform returns.
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	decoded, err := ir.DecodeValue(generic)
	if err != nil {
		return nil, err
	}
	return p.value(decoded)
}

// escape doubles every `$`, which Pulumi YAML reads back as one, so no
// literal string reads as an interpolation.
func escape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func number(f float64) (*yaml.Node, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("%v is not a JSON number", f)
	}
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(int64(f), 10)}, nil
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(f, 'g', -1, 64)}, nil
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func mapping(content ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: content}
}

func pair(key string, value *yaml.Node) []*yaml.Node { return []*yaml.Node{str(key), value} }

func addPair(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, pair(key, value)...)
}

// wrap breaks a comment into lines of at most 76 characters.
func wrap(text string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > 76 {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n")
}
