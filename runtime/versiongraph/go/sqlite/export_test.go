package sqlite

// SetIDSource makes every id the adapter generates come from fill, which
// fills an id's 16 bytes before the adapter sets its version and variant,
// until the restore it returns runs.
func SetIDSource(fill func(b []byte)) (restore func()) {
	old := idSource
	idSource = fillReader(fill)
	return func() { idSource = old }
}

// fillReader reads as fill fills.
type fillReader func(b []byte)

func (fill fillReader) Read(b []byte) (int, error) {
	fill(b)
	return len(b), nil
}

// SetRollbackStatement makes a binding end the transactions it began with
// statement, as a ROLLBACK that fails when statement does not parse, until
// the restore it returns runs.
func SetRollbackStatement(statement string) (restore func()) {
	old := rollbackStatement
	rollbackStatement = statement
	return func() { rollbackStatement = old }
}
