package sqlutil

// Actor is who the ORMs record as having made a write (D87), beside the
// acting user a caller sets: the system actor, which asSystem (TypeScript)
// and AsSystem (Go) act as for a write no request's caller makes, and
// whether an update of a table with updatedBy needs an acting user. Both
// ORMs of a database are generated from the one value, from its DB
// service's outputs.sql.
type Actor struct {
	// SystemID is the system actor's user ID, a UUID, hyphenated, and
	// SystemName the name an app shows for it.
	SystemID   string
	SystemName string
	// SystemSet reports whether the DB service names the system actor;
	// the TypeScript ORM's definition carries it only then, and the
	// runtime's default otherwise, which is the same.
	SystemSet bool
	// RequireUpdatedBy makes an update of a table with an updatedBy field
	// need an acting user, as a create of a table with createdBy does.
	RequireUpdatedBy bool
}
