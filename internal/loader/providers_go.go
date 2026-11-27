package loader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// A Go runtime provider (D184). A provider's directory, as an API's config
// lists it, holds a Go module when it has a go.mod: the module a Go server
// imports, whose root package exports Definition() (runtime/http/go
// providers). The directory declares the provider once, in its
// package.json (D159) or its Cargo.toml (D185), or, for a provider of Go
// alone, in its
// superschematic.json, whose "provider" object is the package.json's
// "superschematic": {"provider": ...}: a go.mod has no place for it.

// readProvider reads the runtime provider dir holds: the one its
// superschematic.json declares, the one its package.json or Cargo.toml
// declares (ReadRuntimeProvider, D185), which then declare the same, and
// the Go module its go.mod names.
func readProvider(dir string) (*ir.RuntimeProvider, error) {
	fromGo, err := readGoProviderDeclaration(dir)
	if err != nil {
		return nil, err
	}
	var p *ir.RuntimeProvider
	_, noPackage := os.Stat(filepath.Join(dir, "package.json"))
	_, noCrate := os.Stat(filepath.Join(dir, "Cargo.toml"))
	if fromGo == nil || noPackage == nil || noCrate == nil {
		if p, err = ReadRuntimeProvider(dir); err != nil {
			if fromGo == nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w; its superschematic.json declares a provider too, and a directory that holds both declares one provider in each (D184)", err)
		}
	}
	switch {
	case p == nil:
		p = fromGo
	case fromGo != nil:
		same := *fromGo
		same.Package, same.Crate = p.Package, p.Crate
		a, _ := json.Marshal(p)
		b, _ := json.Marshal(&same)
		if !bytes.Equal(a, b) {
			return nil, fmt.Errorf("%s: %s and superschematic.json declare different providers (%s and %s); a directory that holds both declares one provider in each (D184)", dir, strings.TrimSpace(p.Package+" "+p.Crate), a, b)
		}
	}
	module, err := goModulePath(dir)
	if err != nil {
		return nil, err
	}
	p.Module = module
	return p, nil
}

// readGoProviderDeclaration reads the provider dir's superschematic.json
// declares, or nil for a directory with none.
func readGoProviderDeclaration(dir string) (*ir.RuntimeProvider, error) {
	data, err := os.ReadFile(filepath.Join(dir, "superschematic.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Provider json.RawMessage `json:"provider"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: superschematic.json: %w", dir, err)
	}
	if len(doc.Provider) == 0 {
		return nil, fmt.Errorf("%s: superschematic.json declares no provider; it names the interface the provider implements under \"provider\": {\"name\", \"implements\", ...} (D184)", dir)
	}
	dec := json.NewDecoder(bytes.NewReader(doc.Provider))
	dec.DisallowUnknownFields()
	var m providerManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: superschematic.json: provider: %w; it takes name, implements, operations, config and role", dir, err)
	}
	p := &ir.RuntimeProvider{
		Name:       m.Name,
		Implements: m.Implements,
		Path:       filepath.ToSlash(dir),
		Operations: append([]string(nil), m.Operations...),
		Role:       append([]string(nil), m.Role...),
	}
	keys := make([]string, 0, len(m.Config))
	for key := range m.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := m.Config[key]
		p.Config = append(p.Config, ir.ProviderConfigKey{Key: key, Secret: item.Secret, Optional: item.Optional})
	}
	if err := p.Check(); err != nil {
		return nil, err
	}
	p.Normalize()
	p.Path = ""
	return p, nil
}

// goModulePath is the module path dir's go.mod names, "" for a directory
// with no go.mod.
func goModulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			path := strings.TrimSpace(rest)
			if i := strings.Index(path, "//"); i >= 0 {
				path = strings.TrimSpace(path[:i])
			}
			if unquoted, err := strconv.Unquote(path); err == nil {
				path = unquoted
			}
			if path == "" {
				break
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("%s: go.mod names no module", dir)
}
