// Package graphroutes describes a version graph as its routes serve it
// (D138 in docs/DECISIONS.md): the member kinds of a graph a
// @graphOperations set serves, each with the members of its row, the
// content fields a typed upsert of the generated facade writes. The
// loader's expansion types the rows from it (internal/loader/graphops),
// and the TypeScript server's router hands it to the runtime's handlers
// (tsrestgen), so the two agree on what a row holds.
package graphroutes

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	ir "github.com/parable-work/superschematic/ir"
)

// Relation is a member's field that holds a to-one relation to a table:
// the row's member that holds the relation's key, the member type's
// field, and the target's key field, which the facade's typed value holds
// the key under.
type Relation struct {
	Member string
	Field  string
	Key    string
}

// Member is one member kind of a graph as the routes serve it.
type Member struct {
	// Type is the member's type name in the database (Step).
	Type string
	// Kind is the member's kind in the graph's descriptor and in the
	// kind enum (step).
	Kind string
	// Tree is the member of a tree and of the edits that holds its rows
	// (step).
	Tree string
	// Fields are the members of its row beside entityKey, in the member
	// type's field order: the content fields a typed upsert writes.
	Fields []string
	// Relations are the fields among them that hold a to-one relation's
	// key.
	Relations []Relation
	// Ordered reports that the kind's order is a string key, which
	// arrange places.
	Ordered bool
}

// TreeMember is the member of a tree and of a graph's edits that holds
// the rows of the member type typeName, as the generated facade names it:
// the type's name with its first letter lower-cased (step, lineItem).
func TreeMember(typeName string) string {
	if typeName == "" {
		return ""
	}
	return strings.ToLower(typeName[:1]) + typeName[1:]
}

// Graph finds the version graph of db whose root is the type named root.
func Graph(db *ir.Schema, root string) (graphdesc.Graph, error) {
	graphs, err := graphdesc.Graphs(db)
	if err != nil {
		return graphdesc.Graph{}, err
	}
	for _, g := range graphs {
		if g.Root != nil && g.Root.Name == root {
			return g, nil
		}
	}
	return graphdesc.Graph{}, fmt.Errorf("%s declares no version graph rooted at %s", db.Name, root)
}

// Members are the member kinds of g as the routes serve them, read from
// db, the database that declares the graph, in descriptor order.
func Members(db *ir.Schema, g graphdesc.Graph) []Member {
	out := make([]Member, 0, len(g.Members))
	for i, member := range g.Members {
		described := g.Descriptor.Kinds[i]
		m := Member{Type: member.Type.Name, Kind: member.Kind, Tree: TreeMember(member.Type.Name), Ordered: described.OrderIsKey()}
		for _, fd := range ContentFields(db, member, described) {
			name := fd.Name
			if target := RelationTarget(db, fd); target != nil {
				name = RelationMember(fd.Name)
				m.Relations = append(m.Relations, Relation{Member: name, Field: fd.Name, Key: KeyField(target).Name})
			}
			m.Fields = append(m.Fields, name)
		}
		out = append(out, m)
	}
	return out
}

// RelationMember is the member of a row that holds the key of the to-one
// relation field, as a view and a repository's row name it (authorId).
func RelationMember(field string) string {
	return field + "Id"
}

// ContentFields are the fields of a member a typed upsert of the
// generated facade writes, beside its entity key: every field that is a
// column of the member's table but its row id, the version, the ref, the
// root, the tombstone and the audit fields, which the engine and its
// adapter write.
func ContentFields(db *ir.Schema, member graphdesc.Member, described graphdesc.Kind) []*ir.FieldDef {
	var out []*ir.FieldDef
	for _, fd := range member.Type.Fields {
		column := graphdesc.Column(db, fd)
		if _, ok := described.Columns[column]; !ok {
			// A list of relations, which a join or the other table holds.
			continue
		}
		switch {
		case fd.Key, fd.InternalMetadata, isAuditField(fd.Name):
		case fd.Origin == ir.OriginVersionGraph:
			// entityKey, ref and deletedOnRef: the row names its entity
			// key apart, and the engine writes the others.
		case column == member.RootColumn:
		default:
			out = append(out, fd)
		}
	}
	return out
}

// isAuditField reports whether a field is an audit field the adapter
// writes, as the generated facade names them.
func isAuditField(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy", "deletedAt", "deletedBy":
		return true
	}
	return false
}

// RelationTarget is the table fd is a to-one relation to, or nil.
func RelationTarget(db *ir.Schema, fd *ir.FieldDef) *ir.TypeDef {
	target := db.Types[fd.TypeRef.Name]
	if target == nil || target.Role != ir.RoleDBTable || target.JsonField || fd.JsonField || fd.TypeRef.IsArray || fd.TypeRef.IsMap {
		return nil
	}
	if KeyField(target) == nil {
		return nil
	}
	return target
}

// KeyField returns td's @key field, else its field named id, or nil.
func KeyField(td *ir.TypeDef) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd.Key {
			return fd
		}
	}
	for _, fd := range td.Fields {
		if fd.Name == "id" {
			return fd
		}
	}
	return nil
}
