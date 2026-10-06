// Package pintool writes an extension's pinned provider schemas
// (docs/stack-model.md, section 6.4, and package providerschema) into the
// directory of its pin file, one file per type the pin lists, from the
// release of the Pulumi provider it pins:
//
//   - schema.json, the provider's Pulumi package schema, gives each type's
//     input properties and the object types they reference;
//   - bridge-metadata.json, the bridged provider's published mapping, gives
//     the Terraform resource type each token is the current name of, and
//     the Terraform name of every list and block property.
//
// Both are fetched from the provider's repository at the pinned tag
// (pulumi/pulumi-<package>) and checked against the digests the pin
// records, through a cache under the user cache directory, since
// schema.json can be about 90 MB.
//
// A property's Terraform name is its Pulumi name in snake case, except a
// list the bridge pluralized (`env` is `envs`), which the mapping's list
// fields name. The tool fails on a list property no mapping field names,
// rather than guess.
//
// Each extension runs it from a command of its own,
// internal/tools/providerschemas, whose main calls Main with its pin file.
// With -check the command exits 1 when a committed file differs from what
// it would write, which is how CI keeps the files in step with the pin.
// With -version it moves the pin to another release and records the new
// digests.
package pintool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/parable-work/superschematic/stack/providerschema"
)

// sourceURL is where an upstream file of a Pulumi package is fetched at a
// version: the package, the version, the package again and the file.
const sourceURL = "https://raw.githubusercontent.com/pulumi/pulumi-%s/v%s/provider/cmd/pulumi-resource-%s/%s"

// The upstream files, in the order the pin lists them.
const (
	schemaFile   = "schema.json"
	metadataFile = "bridge-metadata.json"
)

// Command is how an extension's command runs, from the extension's
// directory.
const Command = "go run ./internal/tools/providerschemas"

// Main is an extension's providerschemas command. pinFile is the name of
// the extension's pin file (`pulumi-gcp.json`), in the directory -dir
// names, `schemas` by default. It exits 1 on an error.
func Main(pinFile string) {
	check := flag.Bool("check", false, "exit 1 when a committed file differs from the generated one")
	version := flag.String("version", "", "move the pin to this provider release (without the v) and record its digests")
	dir := flag.String("dir", "schemas", "the directory that holds the pin and the pinned schemas")
	flag.Parse()
	if err := Run(*dir, pinFile, *version, *check, FetchCached); err != nil {
		fmt.Fprintln(os.Stderr, "providerschemas:", err)
		os.Exit(1)
	}
}

// Fetcher returns an upstream file's content at a URL. The digest the pin
// records is checked by Run, not here.
type Fetcher func(url string) ([]byte, error)

// Run writes the schemas the pin file pinFile in dir lists, or with check
// reports each file that differs. A version other than the pin's moves the
// pin there first.
func Run(dir, pinFile, version string, check bool, fetch Fetcher) error {
	pinPath := filepath.Join(dir, pinFile)
	data, err := os.ReadFile(pinPath)
	if err != nil {
		return err
	}
	var pin providerschema.Pin
	if err := json.Unmarshal(data, &pin); err != nil {
		return fmt.Errorf("%s: %w", pinPath, err)
	}
	if pin.Package == "" || providerschema.PinFileName(pin.Package) != pinFile {
		return fmt.Errorf("%s pins package %q; a pin of package P is named %s", pinPath, pin.Package, providerschema.PinFileName("P"))
	}
	if version != "" && version != pin.Version {
		if check {
			return errors.New("-check and -version do not go together")
		}
		pin.Version = version
		pin.Sources = nil
	}
	if pin.Version == "" {
		return fmt.Errorf("%s pins no version; run %s -version X.Y.Z", pinPath, Command)
	}
	slices.Sort(pin.Types)
	pin.Types = slices.Compact(pin.Types)

	contents := map[string][]byte{}
	var sources []providerschema.Source
	for _, name := range []string{schemaFile, metadataFile} {
		url := fmt.Sprintf(sourceURL, pin.Package, pin.Version, pin.Package, name)
		body, err := fetch(url)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		if want := recorded(pin.Sources, name); want != "" && want != digest {
			return fmt.Errorf("%s has digest %s, but %s records %s", url, digest, pinFile, want)
		} else if want == "" && check {
			return fmt.Errorf("%s records no digest for %s; run %s", pinFile, name, Command)
		}
		contents[name] = body
		sources = append(sources, providerschema.Source{Name: name, URL: url, SHA256: digest})
	}
	pin.Sources = sources

	upstream, err := parseUpstream(pin.Package, contents[schemaFile], contents[metadataFile])
	if err != nil {
		return err
	}
	outputs := map[string][]byte{}
	for _, token := range pin.Types {
		s, err := upstream.extract(token)
		if err != nil {
			return err
		}
		if outputs[providerschema.FileName(token)], err = encode(s); err != nil {
			return err
		}
	}
	if outputs[pinFile], err = encode(pin); err != nil {
		return err
	}

	failed := false
	for _, name := range sortedKeys(outputs) {
		path := filepath.Join(dir, name)
		if check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, outputs[name]) {
				fmt.Fprintf(os.Stderr, "%s is out of date; run: %s\n", path, Command)
				failed = true
			}
			continue
		}
		if err := os.WriteFile(path, outputs[name], 0o644); err != nil {
			return err
		}
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, path := range stale {
		if _, ok := outputs[filepath.Base(path)]; ok {
			continue
		}
		if check {
			fmt.Fprintf(os.Stderr, "%s pins a type %s does not list; run: %s\n", path, pinFile, Command)
			failed = true
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if failed {
		return errors.New("the pinned schemas are out of date")
	}
	return nil
}

func recorded(sources []providerschema.Source, name string) string {
	for _, s := range sources {
		if s.Name == name {
			return s.SHA256
		}
	}
	return ""
}

// encode writes v as indented JSON with a final newline, leaving <, > and
// & as they are.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// upstream is what the tool reads from the two upstream files.
type upstream struct {
	// pkg is the Pulumi package.
	pkg string

	resources map[string]json.RawMessage
	types     map[string]json.RawMessage

	// terraform maps a token to the Terraform type it is the current
	// name of.
	terraform map[string]string

	// fields holds each Terraform type's list and block fields.
	fields map[string]map[string]*aliasField
}

// bridgeMetadata is the part of bridge-metadata.json the tool reads: the
// auto-aliasing table, which names each Terraform resource's current token
// and records its list and block fields.
type bridgeMetadata struct {
	AutoAliasing struct {
		Resources map[string]struct {
			Current string                 `json:"current"`
			Fields  map[string]*aliasField `json:"fields"`
		} `json:"resources"`
	} `json:"auto-aliasing"`
}

// aliasField is a Terraform list or block field.
type aliasField struct {
	// MaxItemsOne is true for a block of at most one element, which
	// Pulumi flattens to an object and does not pluralize.
	MaxItemsOne *bool `json:"maxItemsOne"`

	// Elem holds the fields of a block's element.
	Elem *struct {
		Fields map[string]*aliasField `json:"fields"`
	} `json:"elem"`
}

func (f *aliasField) list() bool { return f.MaxItemsOne != nil && !*f.MaxItemsOne }

func (f *aliasField) elemFields() map[string]*aliasField {
	if f == nil || f.Elem == nil {
		return nil
	}
	return f.Elem.Fields
}

func parseUpstream(pkg string, schemaJSON, metadataJSON []byte) (*upstream, error) {
	var schema struct {
		Resources map[string]json.RawMessage `json:"resources"`
		Types     map[string]json.RawMessage `json:"types"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil, fmt.Errorf("%s: %w", schemaFile, err)
	}
	var meta bridgeMetadata
	if err := json.Unmarshal(metadataJSON, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", metadataFile, err)
	}
	u := &upstream{
		pkg:       pkg,
		resources: schema.Resources,
		types:     schema.Types,
		terraform: map[string]string{},
		fields:    map[string]map[string]*aliasField{},
	}
	for _, tf := range sortedKeys(meta.AutoAliasing.Resources) {
		res := meta.AutoAliasing.Resources[tf]
		if res.Current == "" {
			continue
		}
		if prev, dup := u.terraform[res.Current]; dup {
			return nil, fmt.Errorf("%s names both %s and %s as the current name of %s", metadataFile, prev, tf, res.Current)
		}
		u.terraform[res.Current] = tf
		u.fields[tf] = res.Fields
	}
	return u, nil
}

// extract pins one type: its inputs, the types they reach, its Terraform
// name and its renames.
func (u *upstream) extract(token string) (*providerschema.Schema, error) {
	raw, ok := u.resources[token]
	if !ok {
		return nil, fmt.Errorf("pulumi-%s has no resource %s", u.pkg, token)
	}
	var res struct {
		InputProperties map[string]*providerschema.Property `json:"inputProperties"`
		RequiredInputs  []string                            `json:"requiredInputs"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", token, err)
	}
	tf, ok := u.terraform[token]
	if !ok {
		return nil, fmt.Errorf("%s names no Terraform resource whose current name is %s", metadataFile, token)
	}
	s := &providerschema.Schema{
		Token:           token,
		Terraform:       providerschema.Terraform{Type: tf, Renames: map[string]string{}},
		InputProperties: res.InputProperties,
		RequiredInputs:  res.RequiredInputs,
		Types:           map[string]*providerschema.ObjectType{},
	}
	if s.InputProperties == nil {
		s.InputProperties = map[string]*providerschema.Property{}
	}
	sort.Strings(s.RequiredInputs)
	if err := u.collectTypes(s, s.InputProperties); err != nil {
		return nil, fmt.Errorf("%s: %w", token, err)
	}
	if err := renames(s, "", s.InputProperties, u.fields[tf]); err != nil {
		return nil, fmt.Errorf("%s: %w", token, err)
	}
	if len(s.Terraform.Renames) == 0 {
		s.Terraform.Renames = nil
	}
	if len(s.Types) == 0 {
		s.Types = nil
	}
	return s, nil
}

// collectTypes adds every type the properties reference, and the types
// those reference, to s.Types.
func (u *upstream) collectTypes(s *providerschema.Schema, props map[string]*providerschema.Property) error {
	var visit func(p *providerschema.Property) error
	visit = func(p *providerschema.Property) error {
		if p == nil {
			return nil
		}
		if strings.HasPrefix(p.Ref, providerschema.TypeRefPrefix) {
			token := strings.TrimPrefix(p.Ref, providerschema.TypeRefPrefix)
			if _, seen := s.Types[token]; !seen {
				raw, ok := u.types[token]
				if !ok {
					return fmt.Errorf("references type %s, which pulumi-%s does not declare", token, u.pkg)
				}
				var t providerschema.ObjectType
				if err := json.Unmarshal(raw, &t); err != nil {
					return fmt.Errorf("type %s: %w", token, err)
				}
				sort.Strings(t.Required)
				s.Types[token] = &t
				for _, name := range sortedKeys(t.Properties) {
					if err := visit(t.Properties[name]); err != nil {
						return err
					}
				}
			}
		}
		if err := visit(p.Items); err != nil {
			return err
		}
		if err := visit(p.AdditionalProperties); err != nil {
			return err
		}
		for _, alt := range p.OneOf {
			if err := visit(alt); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range sortedKeys(props) {
		if err := visit(props[name]); err != nil {
			return err
		}
	}
	return nil
}

// renames records the Terraform name of each property under prefix whose
// name differs in Terraform, and descends into the object types the
// properties reference. fields are the Terraform list and block fields at
// this level.
func renames(s *providerschema.Schema, prefix string, props map[string]*providerschema.Property, fields map[string]*aliasField) error {
	for _, name := range sortedKeys(props) {
		p := props[name]
		tf, err := terraformName(name, p, fields)
		if err != nil {
			return fmt.Errorf("property %s%s: %w", prefix, name, err)
		}
		if tf != name {
			s.Terraform.Renames[prefix+name] = tf
		}
		ref := p.Ref
		if p.Type == "array" && p.Items != nil {
			ref = p.Items.Ref
		}
		t := s.Types[strings.TrimPrefix(ref, providerschema.TypeRefPrefix)]
		if !strings.HasPrefix(ref, providerschema.TypeRefPrefix) || t == nil || len(t.Enum) > 0 {
			continue
		}
		if err := renames(s, prefix+name+".", t.Properties, fields[tf].elemFields()); err != nil {
			return err
		}
	}
	return nil
}

// terraformName returns a property's Terraform name: its snake case when
// a field or no list has that name, else the list field the bridge
// pluralized into it.
func terraformName(name string, p *providerschema.Property, fields map[string]*aliasField) (string, error) {
	snake := toSnake(name)
	if _, ok := fields[snake]; ok || p.Type != "array" {
		return snake, nil
	}
	var matches []string
	for _, field := range sortedKeys(fields) {
		if fields[field].list() && slices.Contains(plurals(toCamel(field)), name) {
			matches = append(matches, field)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("is a list, and no list field of the Terraform resource is named %s or pluralizes to it", snake)
	}
	return "", fmt.Errorf("is a list that the Terraform fields %s all pluralize to", strings.Join(matches, ", "))
}

// plurals returns the plural forms the bridge gives a list's name.
func plurals(name string) []string {
	out := []string{name + "s", name + "es"}
	if base, ok := strings.CutSuffix(name, "y"); ok {
		out = append(out, base+"ies")
	}
	return out
}

// toSnake turns a camelCase Pulumi name into its snake_case Terraform
// form: `invokerIamDisabled` is `invoker_iam_disabled`.
func toSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// toCamel turns a snake_case Terraform name into camelCase.
func toCamel(name string) string {
	var b strings.Builder
	upper := false
	for _, r := range name {
		if r == '_' {
			upper = true
			continue
		}
		if upper {
			r = unicode.ToUpper(r)
			upper = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FetchCached fetches a URL once into the user cache directory, keyed by
// the URL's path, and reads it from there afterwards.
func FetchCached(url string) ([]byte, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "superschematic", "providerschemas", filepath.FromSlash(strings.TrimPrefix(url, "https://")))
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	return data, os.Rename(tmp, path)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
