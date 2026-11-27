// Package projectbehaviors reads the behaviors a project declares (D103's
// step 5): the directory its superschematic.toml names (behaviors), which a
// binary registers before it reads a schema, so a table implements one of
// them, or carries it on a field, as it does the core's.
//
// The directory holds a behavior's class, a *.behavior.ts file of
// abstract classes marked @declaresBehavior, read by the TypeScript reader
// through the tsconfig.json beside it (tsreader.ReadBehaviorClasses), or
// its declaration as data, a *.behavior.json or *.behavior.yaml file: the
// document `superschematic behaviors` writes, with a crate member beside
// the declaration's when a Rust crate implements its hooks over the core's
// API ({ crate, path, register }, as a class's crate option is). A
// behavior is named <prefix>.<Name>, its prefix the owner it registers
// under, as an extension's are.
package projectbehaviors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/registry"
)

// The suffixes of a behavior's declaration as data.
const (
	jsonSuffix = ".behavior.json"
	yamlSuffix = ".behavior.yaml"
)

// crateMember is the member of a declaration as data that names the Rust
// crate whose hooks run it on the core.
const crateMember = "crate"

// Specs reads dir's behaviors into the specs a registry registers, in
// file name order: each class of its *.behavior.ts files, then each
// declaration its *.behavior.json and *.behavior.yaml files hold. reg
// answers which packages are authoring packages, for the classes. A
// behavior whose name is not <prefix>.<Name> is refused, naming its file,
// as are a dir that holds none and a file that does not read.
func Specs(dir string, reg *registry.Registry) ([]registry.BehaviorSpec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("behaviors: the project's behaviors directory: %w", err)
	}
	var classes bool
	var data []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
		case strings.HasSuffix(name, tsreader.BehaviorClassSuffix):
			classes = true
		case strings.HasSuffix(name, jsonSuffix), strings.HasSuffix(name, yamlSuffix):
			data = append(data, name)
		}
	}
	sort.Strings(data)
	var specs []registry.BehaviorSpec
	if classes {
		read, err := tsreader.ReadBehaviorClasses(dir, reg)
		if err != nil {
			return nil, fmt.Errorf("behaviors: %s: %w", dir, err)
		}
		for _, class := range read {
			declaration, err := registry.CanonicalDeclaration(class.Declaration)
			if err != nil {
				return nil, fmt.Errorf("behaviors: %s: class %s: %w", class.File, class.Class, err)
			}
			spec, err := specOf(class.File, class.Declaration.Name, declaration, class.Rules)
			if err != nil {
				return nil, err
			}
			specs = append(specs, spec)
		}
	}
	for _, name := range data {
		spec, err := readData(filepath.Join(dir, name), name)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("behaviors: %s declares no behavior: it holds no *.behavior.ts class and no *.behavior.json or *.behavior.yaml declaration", dir)
	}
	return specs, nil
}

// readData reads the declaration a *.behavior.json or *.behavior.yaml
// file holds, and the crate its crate member names.
func readData(path, file string) (registry.BehaviorSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %w", err)
	}
	if strings.HasSuffix(file, yamlSuffix) {
		var value any
		if err := yaml.Unmarshal(raw, &value); err != nil {
			return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: %w", file, err)
		}
		if raw, err = json.Marshal(value); err != nil {
			return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: converting YAML to JSON: %w", file, err)
		}
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: a behavior's declaration is a JSON object: %w", file, err)
	}
	var crate registry.BehaviorRust
	if text, ok := members[crateMember]; ok {
		var named struct {
			Crate    string `json:"crate"`
			Path     string `json:"path"`
			Register string `json:"register"`
		}
		dec := json.NewDecoder(bytes.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&named); err != nil {
			return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: crate is { crate, path, register }: %w", file, err)
		}
		crate = registry.BehaviorRust{Crate: named.Crate, Path: named.Path, Register: named.Register}
		delete(members, crateMember)
	}
	declaration, err := json.Marshal(members)
	if err != nil {
		return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: %w", file, err)
	}
	var name struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(declaration, &name)
	return specOf(file, name.Name, declaration, crate)
}

// specOf is the spec of the behavior name that file declares, registered
// under its prefix.
func specOf(file, name string, declaration []byte, crate registry.BehaviorRust) (registry.BehaviorSpec, error) {
	prefix, bare, qualified := strings.Cut(name, ".")
	if !qualified || prefix == "" || bare == "" {
		return registry.BehaviorSpec{}, fmt.Errorf("behaviors: %s: behavior %q is a project's, named <prefix>.<Name>: its prefix keeps it apart from the core's and an extension's (D103)", file, name)
	}
	return registry.BehaviorSpec{Extension: prefix, Rules: crate, Declaration: declaration}, nil
}
