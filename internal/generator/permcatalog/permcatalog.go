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
// openapi.json. An API whose operations name no permission has no catalog,
// and none of its outputs change.
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

// Version is the catalog format this package writes.
const Version = 1

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
	// it, sorted in byte order.
	Operations []string `json:"operations"`
}

// Operation is what the catalog reads of one operation of the API.
type Operation struct {
	// ID is the operation's OpenAPI operation id.
	ID string
	// Permissions are the permissions its route requires
	// (@requirePermission, or the administration route's own).
	Permissions []string
	// Identity: it is an operation of the user model's routes, which the
	// identity runtime serves.
	Identity bool
}

// Build returns the catalog of the API named api, whose authDb is authDB,
// from its operations, and false when no operation names a permission.
func Build(api, authDB string, operations []Operation) (Catalog, bool) {
	byName := map[string]*Permission{}
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
	c := Catalog{Version: Version, API: api, AuthDB: authDB}
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

// Parse reads a catalog. It refuses unknown members, a version other than
// Version, and a catalog that names no API.
func Parse(data []byte) (Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("permcatalog: %w", err)
	}
	if c.Version != Version {
		return Catalog{}, fmt.Errorf("permcatalog: version %d, want %d", c.Version, Version)
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
