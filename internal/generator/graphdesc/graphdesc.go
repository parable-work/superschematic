// Package graphdesc builds the descriptor of each version graph a schema
// declares (D17): the JSON that tells the version-graph core which column of
// a member kind's rows plays which role, how each column merges, and which
// columns are not content. runtime/versiongraph/README.md is its contract.
//
// The ORM generator writes a graph's descriptor as a constant beside its
// generated shell, and the Go types generator writes it as
// versiongraph/<name>.json beside the types, so a browser or another
// runtime drives the core with the same descriptor. Both read it from here.
package graphdesc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The role columns every member kind has once the loader has expanded its
// graph (internal/loader/versiongraph).
const (
	EntityKeyColumn = "entity_key"
	RefColumn       = "ref_id"
	TombstoneColumn = "deleted_on_ref"
	VersionColumn   = "_version"
)

// Descriptor is a graph descriptor as the core reads it.
type Descriptor struct {
	Graph string `json:"graph"`
	Kinds []Kind `json:"kinds"`
}

// Kind describes one member kind's rows.
type Kind struct {
	Kind      string            `json:"kind"`
	Key       string            `json:"key"`
	ID        string            `json:"id"`
	Ref       string            `json:"ref"`
	Tombstone string            `json:"tombstone"`
	Version   string            `json:"version"`
	Author    string            `json:"author,omitempty"`
	Parent    *Parent           `json:"parent,omitempty"`
	Order     string            `json:"order,omitempty"`
	Singleton bool              `json:"singleton,omitempty"`
	Units     map[string]string `json:"units,omitempty"`
	Excluded  []string          `json:"excluded,omitempty"`
}

// Parent is a kind's containment edge: the column holding the parent row's
// entity key, and the parent's kind.
type Parent struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
}

// Graph is one version graph of a schema, as the generators need it.
type Graph struct {
	// Name is the PascalCase name the generated types carry ("Recipe").
	Name string
	// FileName is the snake_case name: the table prefix, the descriptor's
	// graph name and the stem of the files written for it ("recipe").
	FileName string
	// Root is the @versionGraph type.
	Root *ir.TypeDef
	// Members are the @graphMember types, in type name order, which is
	// descriptor order.
	Members []Member
	// Descriptor is the graph's descriptor.
	Descriptor Descriptor
}

// Member is one member type and the columns the generated shell writes
// through besides its own content.
type Member struct {
	Type *ir.TypeDef
	// Kind is the member's kind name in the descriptor and the
	// <Name>EntityKind enum ("step").
	Kind string
	// IDColumn is the column of the member's @key.
	IDColumn string
	// RootColumn is the column of the member's relation to the root.
	RootColumn string
}

// JSON is the descriptor as written to versiongraph/<name>.json and into the
// ORM constant: two-space indented, with a trailing newline.
func (d Descriptor) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("graphdesc: encode the %s descriptor: %w", d.Graph, err)
	}
	return buf.Bytes(), nil
}

// Graphs returns every version graph schema declares, by root type name. It
// reads the expanded schema, so each member already has its graph fields.
func Graphs(schema *ir.Schema) []Graph {
	var roots []*ir.TypeDef
	for _, td := range schema.Types {
		if td.VersionGraph != nil {
			roots = append(roots, td)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })

	graphs := make([]Graph, 0, len(roots))
	for _, root := range roots {
		name := root.VersionGraphName()
		g := Graph{Name: name, FileName: codegen.ToSnakeCase(name), Root: root}
		var members []*ir.TypeDef
		for _, td := range schema.Types {
			if td.GraphMember != nil && td.GraphMember.Graph == root.Name {
				members = append(members, td)
			}
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
		g.Descriptor.Graph = g.FileName
		for _, td := range members {
			member, kind := describe(schema, root, td)
			g.Members = append(g.Members, member)
			g.Descriptor.Kinds = append(g.Descriptor.Kinds, kind)
		}
		graphs = append(graphs, g)
	}
	return graphs
}

// describe builds one member's descriptor entry.
func describe(schema *ir.Schema, root, td *ir.TypeDef) (Member, Kind) {
	member := Member{Type: td, Kind: codegen.ToSnakeCase(td.Name)}
	kind := Kind{
		Kind:      member.Kind,
		Key:       EntityKeyColumn,
		Ref:       RefColumn,
		Tombstone: TombstoneColumn,
		Version:   VersionColumn,
		Singleton: td.GraphMember.Singleton,
	}
	columns := map[string]string{}
	for _, fd := range td.Fields {
		columns[fd.Name] = Column(schema, fd)
	}
	if _, ok := columns["updatedBy"]; ok {
		kind.Author = columns["updatedBy"]
	}
	if p := td.GraphMember.Parent; p != nil {
		kind.Parent = &Parent{Key: columns[p.Key], Kind: codegen.ToSnakeCase(p.Of)}
	}
	if td.GraphMember.Order != "" {
		kind.Order = columns[td.GraphMember.Order]
	}
	for _, fd := range td.Fields {
		column := columns[fd.Name]
		switch {
		case fd.Key:
			member.IDColumn = column
			kind.ID = column
		case fd.Origin == ir.OriginVersionGraph:
			// entityKey, ref and deletedOnRef are role columns.
		case fd.TypeRef.Name == root.Name:
			// The graph's own column: every row of the graph names its root.
			member.RootColumn = column
			kind.Excluded = append(kind.Excluded, column)
		case isAudit(fd.Name):
			if column != kind.Author {
				kind.Excluded = append(kind.Excluded, column)
			}
		case fd.ConflictUnit == ir.ConflictUnitExcluded:
			kind.Excluded = append(kind.Excluded, column)
		case fd.ConflictUnit == ir.ConflictUnitKeyed || fd.ConflictUnit == ir.ConflictUnitJSONSchema:
			if kind.Units == nil {
				kind.Units = map[string]string{}
			}
			kind.Units[column] = fd.ConflictUnit
		}
	}
	return member, kind
}

// Column is the column that holds fd, as the sql generator names it: a
// to-one relation to a table is <field>_id, every other field its
// snake_case name.
func Column(schema *ir.Schema, fd *ir.FieldDef) string {
	name := codegen.ToSnakeCase(fd.Name)
	target := schema.Types[fd.TypeRef.Name]
	if target != nil && target.Role == ir.RoleDBTable && !target.JsonField && !fd.JsonField && !fd.TypeRef.IsArray && !fd.TypeRef.IsMap {
		return name + "_id"
	}
	return name
}

// isAudit reports whether a field is one of the audit fields D17 leaves out
// of a member's content without a decorator.
func isAudit(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy":
		return true
	}
	return false
}
