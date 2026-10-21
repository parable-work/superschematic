package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The SQLite files of an environment's sqlite-first databases (D64;
// docs/stack-model.md, section 18.13): each hosted DB schema of a database
// on SQLitePlatform is a file under the environment's state directory,
// `databases/<database>.sqlite`, beside its secrets and its edges' keys,
// so it is the machine's and never the output root's. The provisioner
// creates it empty, which SQLite reads as an empty database, the migration
// runner migrates it from the SQLite plan, and the server opens the file
// its sqlite: URL names. Destroy leaves it for the next run, as it leaves
// the containers' data; Purge removes it.

// ensureSQLiteFile creates the SQLite file at path, and its directory, when
// it is missing, and reports whether it did. An empty file is an empty
// database.
func ensureSQLiteFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

// removeSQLiteFile removes the SQLite file at path with the journal files
// SQLite keeps beside it, and reports whether the database was there.
func removeSQLiteFile(path string) (bool, error) {
	_, err := os.Stat(path)
	existed := err == nil
	var errs []error
	for _, file := range []string{path, path + "-journal", path + "-wal", path + "-shm"} {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return existed, errors.Join(errs...)
}

// sqliteOutputs adds each SQLite database's outputs, its file's url and
// path under the state directory dir, to out.
func (prog *Program) sqliteOutputs(dir string, out map[string]map[string]any) {
	for _, db := range prog.SQLiteDatabases {
		file := db.path(dir)
		out[db.ID] = map[string]any{"name": db.Name, "path": file, "url": SQLiteURL(file)}
	}
}
