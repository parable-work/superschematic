package ir

// An operation the build serves from a table (D106). An API marks a method
// of an operation set @tableOperation(), from @superschematic/api:
//
//	@rest(HttpMethod.PATCH, "issues/{id}")
//	@requirePermission(["issues.write"])
//	@tableOperation()
//	updateIssue(id: Identity.UUID, input: UpdateIssueInput): IssueView {
//	  throw new Error("schema declaration only");
//	}
//
// The method stays an ordinary declaration: its route, its guards, its
// arguments and its result are the ones it writes, and the OpenAPI
// document, the SDKs, the permission catalog and the tool documents read
// it as any operation. The reader records the decorator as
// FieldDef.TableOperation, empty but for the row rule its argument writes
// (By, OverridePermission, D108). The loader holds the method's views to one
// table of the API's database, decides what the operation does from its
// HTTP method and shape, and fills the rest. A server whose generator
// writes table handlers routes the operation to one and leaves the method
// out of the implementation's interface and its scaffold; any other keeps
// the method, to be written by hand.

// The kinds of a table operation, which its HTTP method and shape decide.
const (
	// TableOperationRead is a GET whose path holds the table's key and
	// whose result is one view: the row.
	TableOperationRead = "read"
	// TableOperationList is a GET whose result is a list of views: a page
	// of the rows.
	TableOperationList = "list"
	// TableOperationCreate is a POST with one body argument and no key:
	// a new row.
	TableOperationCreate = "create"
	// TableOperationUpdate is a PATCH with the key and one body argument:
	// the members the body sets.
	TableOperationUpdate = "update"
	// TableOperationDelete is a DELETE with the key: the row, soft-deleted
	// on a table with deletedAt.
	TableOperationDelete = "delete"
)

// TableOperationKinds are the kinds of a table operation.
var TableOperationKinds = []string{
	TableOperationRead, TableOperationList, TableOperationCreate, TableOperationUpdate, TableOperationDelete,
}

// TableOperation is @tableOperation on an operation (D106). The decorator's
// argument writes the row rule, By and OverridePermission (D108); every
// other member is the loader's: a data form leaves them out, and one that
// writes a member the loader would fill otherwise is refused.
type TableOperation struct {
	// Kind is what the handler does, one of [TableOperationKinds].
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Database is the DB service the table belongs to: the API's database
	// ([Schema.Database]).
	Database string `json:"database,omitempty" yaml:"database,omitempty"`

	// Type is the table the operation's views are @source views of, by its
	// name in the database: a table, or for a read and a list a projection
	// (D90).
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Projection is true when Type is a projection, which is read through
	// its own repository and never written.
	Projection bool `json:"projection,omitempty" yaml:"projection,omitempty"`

	// Key is the table's key field: the path parameter of a read, an
	// update and a delete, by the same name. Empty for a list and a
	// create.
	Key string `json:"key,omitempty" yaml:"key,omitempty"`

	// Parents are the path parameters beside the key, each named for a
	// relation's key of the table (teams/{teamId}/issues): a create sets
	// the relation from it, and every other kind keeps only rows whose
	// relation is that row.
	Parents []TableOperationParent `json:"parents,omitempty" yaml:"parents,omitempty"`

	// Input is the body argument of a create and an update, by its name;
	// its type is a @source view of the table.
	Input string `json:"input,omitempty" yaml:"input,omitempty"`

	// By is the row rule (D108): the members of the row that name who may
	// reach it, each as a view names it, an own field that holds a subject
	// (userId, owner), a to-one relation's key (assigneeId) or createdBy.
	// A read, a list, an update and a delete reach only the rows one of
	// them names the caller in, and a create sets each but createdBy to
	// the caller. Empty reaches every row the route admits.
	By []string `json:"by,omitempty" yaml:"by,omitempty"`

	// OverridePermission is the permission whose holder By does not hold
	// back: they reach every row, as an operation without By does.
	OverridePermission string `json:"overridePermission,omitempty" yaml:"overridePermission,omitempty"`
}

// TableOperationParent is one path parameter that holds a table
// operation's row to a related row.
type TableOperationParent struct {
	// Param is the path parameter, the relation's key (teamId).
	Param string `json:"param" yaml:"param"`

	// Relation is the table's to-one relation the parameter is the key of
	// (team).
	Relation string `json:"relation" yaml:"relation"`
}

// Writes reports whether the operation changes the table: a create, an
// update or a delete.
func (t *TableOperation) Writes() bool {
	if t == nil {
		return false
	}
	switch t.Kind {
	case TableOperationCreate, TableOperationUpdate, TableOperationDelete:
		return true
	}
	return false
}

// Parent finds the parent a path parameter names, and whether it names
// one.
func (t *TableOperation) Parent(param string) (TableOperationParent, bool) {
	if t == nil {
		return TableOperationParent{}, false
	}
	for _, parent := range t.Parents {
		if parent.Param == param {
			return parent, true
		}
	}
	return TableOperationParent{}, false
}

// HasTableOperations reports whether any operation of the schema is a
// @tableOperation.
func (s *Schema) HasTableOperations() bool {
	if s == nil {
		return false
	}
	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.TableOperation != nil {
				return true
			}
		}
	}
	return false
}
