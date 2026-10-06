package sqlite

// SetIDSource makes every id the adapter generates come from fill, which
// fills an id's 16 bytes before the adapter sets its version and variant,
// until the restore it returns runs.
func SetIDSource(fill func(b []byte)) (restore func()) {
	old := randRead
	randRead = func(b []byte) (int, error) {
		fill(b)
		return len(b), nil
	}
	return func() { randRead = old }
}

// SetRollbackStatement makes a binding end the transactions it began with
// statement, as a ROLLBACK that fails when statement does not parse, until
// the restore it returns runs.
func SetRollbackStatement(statement string) (restore func()) {
	old := rollbackStatement
	rollbackStatement = statement
	return func() { rollbackStatement = old }
}
