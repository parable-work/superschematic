// Package permcatalog builds an API's permission catalog, the
// permissions.json the build writes beside the API's OpenAPI document
// (D50): every permission the API's operations name, with the operations
// that name it. README.md in this directory is its contract.
//
// The catalog informs; it does not decide. A role is not refused for a
// permission no catalog lists, since an engine publishes schemas, and the
// permissions their behaviors name, at run time. A role editor reads it to
// offer the permissions an API checks, and superschematic identity
// bootstrap reads the project's catalogs to warn about a permission none
// of them names.
//
// apigen builds the catalog with the rest of an API's output, and each
// server writer, Go, TypeScript and Rust, writes it as it writes
// openapi.json. An API whose operations name no permission, and whose
// server mounts no engine that asks one, has no catalog, and none of its
// outputs change.
//
// An API whose database holds engine types, which its server mounts the
// engine over (D58), lists what the engine's permission policy asks of
// each: <Type>.define, <Type>.publish, <Type>.read and <Type>.write, each
// with the engine schema and action it is (Permission.Engine). Such a
// catalog is version 2 (EngineVersion); every other is version 1, so a
// reader that predates the engine's permissions reads every catalog it
// read before.
package permcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// FileName is the catalog's name in an API's output directory, beside
// openapi.json.
const FileName = "permissions.json"

// Version is the catalog format this package writes for a catalog that
// lists no engine permission.
const Version = 1

// EngineVersion is the catalog format of a catalog that lists an engine
// permission: version 1 and each permission's engine member.
const EngineVersion = 2

// Catalog is an API's permission catalog.
type Catalog struct {
	Version int `json:"version"`
	// API is the API service's name.
	API string `json:"api"`
	// AuthDB is the DB service whose roles a caller's permissions come
	// from, the API's authDb; empty when the API names none.
	AuthDB string `json:"authDb,omitempty"`
	// Permissions are sorted by name, in byte order.
	Permissions []Permission `json:"permissions"`
}

// Permission is one permission the API's operations name.
type Permission struct {
	Name string `json:"name"`
	// Identity: an operation of the user model's routes needs it, one of
	// the administration permissions under identity_permission_prefix.
	Identity bool `json:"identity"`
	// Operations are the OpenAPI operation ids of the operations that name
	// it, sorted in byte order: none for a permission the engine alone
	// asks.
	Operations []string `json:"operations"`
	// Engine is the question of the engine's permission policy the
	// permission answers, when the API's server mounts the engine over an
	// engine type of its database (D58); nil otherwise.
	Engine *EnginePermission `json:"engine,omitempty"`
}

// EnginePermission is what the engine's permission policy asks of an
// engine type (D58): an action on the schema, in the database's
// namespace. The permission's name is <Schema>.<Action>.
type EnginePermission struct {
	// Database is the DB service whose engine types the schema is.
	Database string `json:"database"`
	// Namespace is the engine namespace the schema is defined in.
	Namespace string `json:"namespace"`
	// Schema is the engine type.
	Schema string `json:"schema"`
	// Action is define, publish, read or write.
	Action string `json:"action"`
}

// Name is the permission the engine's permission policy asks.
func (e EnginePermission) Name() string {
	return e.Schema + "." + e.Action
}

// Operation is what the catalog reads of one operation of the API.
type Operation struct {
	// ID is the operation's OpenAPI operation id.
	ID string
	// Permissions are the permissions its route requires
	// (@requirePermission, or the administration route's own), and for a
	// behavior operation the ones its behavior's config names, which the
	// behaviors' core checks (D103).
	Permissions []string
	// Identity: it is an operation of the user model's routes, which the
	// identity runtime serves.
	Identity bool
}

// Build returns the catalog of the API named api, whose authDb is authDB,
// from its operations and what the engine its server mounts asks, engine,
// and false when neither names a permission.
func Build(api, authDB string, operations []Operation, engine ...EnginePermission) (Catalog, bool) {
	byName := map[string]*Permission{}
	version := Version
	for _, e := range engine {
		name := e.Name()
		if byName[name] == nil {
			copied := e
			byName[name] = &Permission{Name: name, Operations: []string{}, Engine: &copied}
			version = EngineVersion
		}
	}
	for _, op := range operations {
		for _, name := range op.Permissions {
			p := byName[name]
			if p == nil {
				p = &Permission{Name: name}
				byName[name] = p
			}
			p.Identity = p.Identity || op.Identity
			if !slices.Contains(p.Operations, op.ID) {
				p.Operations = append(p.Operations, op.ID)
			}
		}
	}
	if len(byName) == 0 {
		return Catalog{}, false
	}
	c := Catalog{Version: version, API: api, AuthDB: authDB}
	for _, p := range byName {
		slices.Sort(p.Operations)
		c.Permissions = append(c.Permissions, *p)
	}
	slices.SortFunc(c.Permissions, func(a, b Permission) int { return strings.Compare(a.Name, b.Name) })
	return c, true
}

// JSON is the catalog as permissions.json holds it: two-space indented,
// with a trailing newline.
func (c Catalog) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("permcatalog: encode the catalog: %w", err)
	}
	return buf.Bytes(), nil
}

// Parse reads a catalog of version 1 or 2. It refuses unknown members, an
// engine member in a version 1 catalog, another version, and a catalog
// that names no API.
func Parse(data []byte) (Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("permcatalog: %w", err)
	}
	switch c.Version {
	case Version:
		for _, p := range c.Permissions {
			if p.Engine != nil {
				return Catalog{}, fmt.Errorf("permcatalog: version %d, and permission %s has an engine member, which version %d adds", Version, p.Name, EngineVersion)
			}
		}
	case EngineVersion:
	default:
		return Catalog{}, fmt.Errorf("permcatalog: version %d, want %d or %d", c.Version, Version, EngineVersion)
	}
	if c.API == "" {
		return Catalog{}, fmt.Errorf("permcatalog: the catalog names no API")
	}
	return c, nil
}

// Names are the catalog's permissions' names, sorted.
func (c Catalog) Names() []string {
	names := make([]string, len(c.Permissions))
	for i, p := range c.Permissions {
		names[i] = p.Name
	}
	return names
}
