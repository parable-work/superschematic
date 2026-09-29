package ir

// OriginVersionGraph marks a type, field, enum, index or prune pin the
// loader added when it expanded a version graph (@versionGraph and
// @graphMember). Such definitions are loader output, never authored: the
// data forms have no key for Origin, and the schema writer skips them and
// writes the declarations that produce them instead.
const OriginVersionGraph = "versionGraph"

// Conflict unit strategies of @conflictUnit, the merge unit of a graph
// member's field. An empty FieldDef.ConflictUnit means ConflictUnitAtomic.
const (
	// ConflictUnitAtomic merges the whole field as one unit.
	ConflictUnitAtomic = "atomic"

	// ConflictUnitKeyed merges each top-level key of a JSON object.
	ConflictUnitKeyed = "keyed"

	// ConflictUnitJSONSchema merges a JSON Schema object by its entries:
	// each entry of properties, recursively; each name's membership in
	// required; and every other keyword.
	ConflictUnitJSONSchema = "jsonSchema"

	// ConflictUnitExcluded marks a field that is not content: it never
	// conflicts and is not hashed.
	ConflictUnitExcluded = "excluded"
)

// VersionGraphConfig is the @versionGraph declaration of a graph root: the
// stable identity every member row and every generated graph table
// references. The root itself is never overlaid and is in no commit.
type VersionGraphConfig struct {
	// Name prefixes the generated graph tables (snake_case) and types
	// (PascalCase). Empty means the root type's name; see
	// [TypeDef.VersionGraphName].
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// SchemaEpoch is recorded on every commit of the graph. It changes when
	// the member types change in a way a stored commit must be transformed
	// across. Zero is the first epoch.
	SchemaEpoch int64 `json:"schemaEpoch,omitempty" yaml:"schemaEpoch,omitempty"`

	// SnapshotEvery is how many commits past the nearest snapshot on its
	// chain a commit is snapshotted at: its full pin set is stored, so
	// reading its tree stops there. Nil means DefaultSnapshotEvery; a set
	// value is positive. Verification enforces that, not the data form's
	// JSON Schema, whose subset has no numeric bounds.
	SnapshotEvery *int64 `json:"snapshotEvery,omitempty" yaml:"snapshotEvery,omitempty"`
}

// DefaultSnapshotEvery is a version graph's snapshot interval when
// @versionGraph does not set snapshotEvery.
const DefaultSnapshotEvery = 64

// SnapshotInterval returns the graph's snapshot interval: SnapshotEvery, or
// DefaultSnapshotEvery when it is unset.
func (c *VersionGraphConfig) SnapshotInterval() int64 {
	if c == nil || c.SnapshotEvery == nil {
		return DefaultSnapshotEvery
	}
	return *c.SnapshotEvery
}

// GraphMemberConfig is the @graphMember declaration of one entity kind of a
// version graph.
type GraphMemberConfig struct {
	// Graph names the root type (the @versionGraph type) of the graph.
	Graph string `json:"graph" yaml:"graph"`

	// Parent declares containment: a field of this type holds the parent
	// row's entityKey. Nil means the kind has no parent.
	Parent *GraphParent `json:"parent,omitempty" yaml:"parent,omitempty"`

	// Order names the Int64 field that orders siblings. Empty means the
	// kind is unordered.
	Order string `json:"order,omitempty" yaml:"order,omitempty"`

	// Singleton allows at most one live row of the kind per ref.
	Singleton bool `json:"singleton,omitempty" yaml:"singleton,omitempty"`
}

// GraphParent is the containment edge of a graph member: the field Key
// holds the entityKey of a row of Of, a member type of the same graph (the
// member itself included). Deleting a parent removes its descendants.
type GraphParent struct {
	// Key names the field of the member that holds the parent's entityKey.
	Key string `json:"key" yaml:"key"`

	// Of names the parent's member type.
	Of string `json:"of" yaml:"of"`
}

// VersionGraphName returns the name the graph rooted at td prefixes its
// generated types with: VersionGraph.Name, or the root's own name when that
// is empty. It returns "" when td is not a graph root.
func (td *TypeDef) VersionGraphName() string {
	if td == nil || td.VersionGraph == nil {
		return ""
	}
	if td.VersionGraph.Name != "" {
		return td.VersionGraph.Name
	}
	return td.Name
}
