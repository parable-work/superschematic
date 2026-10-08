package writer

import (
	ir "github.com/parable-work/superschematic/ir"
)

// withoutExpansion returns a view of schema without what the loader added
// when it expanded declarations: the types and enums a version graph
// generates, and the fields, indexes and prune pins it adds to member types
// (ir.OriginVersionGraph), and the tables, fields and indexes the user
// model's traits add and the operations, types and enum its route sets add
// (ir.OriginIdentity). Any Origin marks loader output. The declarations
// stay, so a written schema file expands to the same IR when it is read
// back. schema itself is not modified.
func withoutExpansion(schema *ir.Schema) *ir.Schema {
	expanded := false
	for _, td := range schema.Types {
		if td.Origin != "" || typeHasExpansion(td) {
			expanded = true
		}
	}
	for _, def := range schema.Enums {
		if def.Origin != "" {
			expanded = true
		}
	}
	for _, set := range schema.OperationSets {
		if setHasExpansion(set) {
			expanded = true
		}
	}
	if !expanded {
		return schema
	}

	view := *schema
	view.Types = make(map[string]*ir.TypeDef, len(schema.Types))
	for name, td := range schema.Types {
		switch {
		case td.Origin != "":
		case typeHasExpansion(td):
			view.Types[name] = authoredType(td)
		default:
			view.Types[name] = td
		}
	}
	view.Enums = make(map[string]*ir.EnumDef, len(schema.Enums))
	for name, def := range schema.Enums {
		if def.Origin == "" {
			view.Enums[name] = def
		}
	}
	view.OperationSets = make([]*ir.OperationSet, len(schema.OperationSets))
	for i, set := range schema.OperationSets {
		view.OperationSets[i] = set
		if setHasExpansion(set) {
			view.OperationSets[i] = authoredSet(set)
		}
	}
	return &view
}

// setHasExpansion reports whether an operation set holds an operation the
// loader added.
func setHasExpansion(set *ir.OperationSet) bool {
	for _, op := range set.Operations {
		if op.Origin != "" {
			return true
		}
	}
	return false
}

// authoredSet returns a copy of set without the operations the loader
// added. A route set keeps an empty list, as the data forms write it.
func authoredSet(set *ir.OperationSet) *ir.OperationSet {
	copied := *set
	copied.Operations = []*ir.FieldDef{}
	for _, op := range set.Operations {
		if op.Origin == "" {
			copied.Operations = append(copied.Operations, op)
		}
	}
	return &copied
}

// typeHasExpansion reports whether an authored type carries a field, index
// or prune pin the loader added.
func typeHasExpansion(td *ir.TypeDef) bool {
	for _, fd := range td.Fields {
		if fd.Origin != "" {
			return true
		}
	}
	for _, idx := range td.Indexes {
		if idx.Origin != "" {
			return true
		}
	}
	if cfg := td.VersionedConfig; cfg != nil {
		for _, pin := range cfg.PruneKeepReferencedBy {
			if pin.Origin != "" {
				return true
			}
		}
	}
	return false
}

// authoredType returns a copy of td without the fields, indexes and prune
// pins the loader added.
func authoredType(td *ir.TypeDef) *ir.TypeDef {
	copied := *td
	copied.Fields = nil
	for _, fd := range td.Fields {
		if fd.Origin == "" {
			copied.Fields = append(copied.Fields, fd)
		}
	}
	copied.Indexes = nil
	for _, idx := range td.Indexes {
		if idx.Origin == "" {
			copied.Indexes = append(copied.Indexes, idx)
		}
	}
	if cfg := td.VersionedConfig; cfg != nil {
		copiedCfg := *cfg
		copiedCfg.PruneKeepReferencedBy = nil
		for _, pin := range cfg.PruneKeepReferencedBy {
			if pin.Origin == "" {
				copiedCfg.PruneKeepReferencedBy = append(copiedCfg.PruneKeepReferencedBy, pin)
			}
		}
		copied.VersionedConfig = &copiedCfg
	}
	return &copied
}
