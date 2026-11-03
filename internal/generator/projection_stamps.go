package generator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// ProjectionsLockFile is the file beside a DB service's schema config that
// records each projection migration a build of the service wrote: its
// stamp, the projection that declared it, and the hash of the files the
// stamp names. A stamp it records names those files for good: check and
// build refuse a projection that would write other files under it (D102).
const ProjectionsLockFile = "projections.lock"

// ProjectionsLockVersion is the version of the record's format.
const ProjectionsLockVersion = 1

// ProjectionsLock is a service's record of the projection migrations its
// builds wrote.
type ProjectionsLock struct {
	Version    int              `json:"version"`
	Migrations []BuiltMigration `json:"migrations"`
}

// BuiltMigration is one migration stamp a build wrote: the projection that
// declared it, and the hash of the up and down files it names
// (MigrationHash). A stamp stays recorded when no projection declares it
// any more: a database may have applied it.
type BuiltMigration struct {
	Migration  string `json:"migration"`
	Projection string `json:"projection"`
	Hash       string `json:"hash"`
}

// MigrationHash is the hash of a projection's migration pair: each file's
// name and bytes, as a migration runner reads them.
func MigrationHash(files []sqlgen.MigrationFile) string {
	h := sha256.New()
	for _, file := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", file.Name, len(file.Content))
		_, _ = h.Write(file.Content)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ReadProjectionsLock reads the record at path; a missing file is a nil
// record.
func ReadProjectionsLock(path string) (*ProjectionsLock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock ProjectionsLock
	if err := decoder.Decode(&lock); err != nil {
		return nil, fmt.Errorf("%s: not a projections lock: %w", path, err)
	}
	if lock.Version != ProjectionsLockVersion {
		return nil, fmt.Errorf("%s: lock version %d; this compiler reads version %d", path, lock.Version, ProjectionsLockVersion)
	}
	return &lock, nil
}

// WriteProjectionsLock writes lock to path, its migrations sorted by
// stamp, as indented JSON with a final newline.
func WriteProjectionsLock(path string, lock *ProjectionsLock) error {
	sort.Slice(lock.Migrations, func(i, j int) bool { return lock.Migrations[i].Migration < lock.Migrations[j].Migration })
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// CheckProjectionStamps holds the projection migrations a build of the DB
// service at serviceDir would write to the record beside it (D102), with
// no database and writing nothing, as the build does before it writes:
// check calls it. A schema the sql generator refuses is the build's to
// report, so it checks nothing then.
func CheckProjectionStamps(serviceDir string, schema *ir.Schema, cfg *schemaconfig.SchemaConfig, outputs *registry.Outputs, deps map[string]*ir.Schema, names naming.Naming) error {
	if len(schema.Projections()) == 0 {
		return nil
	}
	output, err := sqlgen.Generate(schema, SQLOptions(cfg, outputs, deps, names))
	if err != nil || output == nil {
		return nil
	}
	_, _, err = checkProjectionStamps(serviceDir, ownMigrationsDir(serviceDir, outputs), output.Projections, nil)
	return err
}

// ownMigrationsDir is the service's own migrations directory, where
// outputs.sql.migrationsDir writes the projection migrations beside the
// service's hand-written ones, else empty: the copy under the output root
// is build output, which no one commits.
func ownMigrationsDir(serviceDir string, outputs *registry.Outputs) string {
	subdir := outputs.SQLMigrationsDir()
	if subdir == "" || serviceDir == "" {
		return ""
	}
	return filepath.Join(serviceDir, filepath.FromSlash(subdir))
}

// checkProjectionStamps holds each view's migration pair to what its stamp
// names already: the files the record beside the service at serviceDir
// holds under it, else the pair in ownMigrationsDir, the service's own
// migrations directory, when that holds one. rewrite lists the stamps
// `build --rewrite-migration` writes again whatever they named. It returns
// the record with every stamp the views write, and whether that differs
// from the record on disk; an error names each projection that would
// change the files under its stamp, and the stamp to give it instead.
func checkProjectionStamps(serviceDir, ownMigrationsDir string, views []sqlgen.ProjectionView, rewrite []string) (*ProjectionsLock, bool, error) {
	if len(views) == 0 && len(rewrite) == 0 {
		return nil, false, nil
	}
	declared := map[string]bool{}
	for _, view := range views {
		declared[view.Migration] = true
	}
	rewritten := map[string]bool{}
	for _, stamp := range rewrite {
		if !declared[stamp] {
			return nil, false, fmt.Errorf("--rewrite-migration %s: no projection of the service declares that migration stamp", stamp)
		}
		rewritten[stamp] = true
	}
	lock, err := ReadProjectionsLock(filepath.Join(serviceDir, ProjectionsLockFile))
	if err != nil {
		return nil, false, err
	}
	if lock == nil {
		lock = &ProjectionsLock{Version: ProjectionsLockVersion}
	}
	recorded := map[string]int{}
	for i, m := range lock.Migrations {
		recorded[m.Migration] = i
	}

	type refusal struct {
		view  sqlgen.ProjectionView
		where string
	}
	var refused []refusal
	changed := false
	for _, view := range views {
		files, err := sqlgen.ProjectionMigrationFiles(view)
		if err != nil {
			return nil, false, err
		}
		entry := BuiltMigration{Migration: view.Migration, Projection: view.TypeName, Hash: MigrationHash(files)}
		if i, ok := recorded[view.Migration]; ok {
			switch {
			case lock.Migrations[i].Hash == entry.Hash:
			case rewritten[view.Migration]:
				lock.Migrations[i] = entry
				changed = true
			default:
				refused = append(refused, refusal{view, "which " + ProjectionsLockFile + " records as built"})
			}
			continue
		}
		// With no record of the stamp, a pair already in the service's
		// own migrations directory is the record: it is committed there,
		// and may be applied.
		if !rewritten[view.Migration] && ownMigrationsDir != "" {
			if name := differingFile(ownMigrationsDir, files); name != "" {
				dir := ownMigrationsDir
				if rel, err := filepath.Rel(serviceDir, dir); err == nil {
					dir = filepath.ToSlash(rel)
				}
				refused = append(refused, refusal{view, "whose " + name + " is already in " + dir + " with other contents"})
				continue
			}
		}
		recorded[view.Migration] = len(lock.Migrations)
		lock.Migrations = append(lock.Migrations, entry)
		changed = true
	}
	if len(refused) == 0 {
		return lock, changed, nil
	}

	// Each refused projection takes a stamp of its own after every one the
	// service knows, so no two collide and a runner that only moves forward
	// still applies it.
	known := make([]string, 0, len(lock.Migrations)+len(views))
	for _, m := range lock.Migrations {
		known = append(known, m.Migration)
	}
	for _, view := range views {
		known = append(known, view.Migration)
	}
	known = append(known, migrationVersions(ownMigrationsDir)...)
	next := latestStamp(known)
	var problems, flags []string
	for _, r := range refused {
		next = nextStamp(next)
		problems = append(problems, fmt.Sprintf("projection %s changed under its migration stamp %s, %s, and an applied migration is never rewritten: give it a new stamp, migration: %q",
			r.view.TypeName, r.view.Migration, r.where, next))
		flags = append(flags, "--rewrite-migration "+r.view.Migration)
	}
	applied := "them"
	if len(refused) == 1 {
		applied = refused[0].view.Migration
	}
	return nil, false, fmt.Errorf("%s. While no database has applied %s, `superschematic build %s %s` writes its files again and records them instead",
		strings.Join(problems, "; "), applied, serviceDir, strings.Join(flags, " "))
}

// differingFile names the first of files that dir holds with other bytes,
// else "".
func differingFile(dir string, files []sqlgen.MigrationFile) string {
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.Name))
		if err == nil && !bytes.Equal(data, file.Content) {
			return file.Name
		}
	}
	return ""
}

// migrationFileName is a golang-migrate file name: the version, an
// underscore, a title, and .up.sql or .down.sql.
var migrationFileName = regexp.MustCompile(`^([0-9]+)_.+\.(up|down)\.sql$`)

// migrationVersions lists the versions of the migration files in dir, the
// service's hand-written ones among them; none when dir is empty or
// missing.
func migrationVersions(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var versions []string
	for _, entry := range entries {
		if m := migrationFileName.FindStringSubmatch(entry.Name()); m != nil {
			versions = append(versions, m[1])
		}
	}
	return versions
}

// stampLayout reads a migration stamp as a time, YYYYMMDDHHMMSS, the form
// golang-migrate's `create -format` writes.
const stampLayout = "20060102150405"

// latestStamp is the greatest of stamps as numbers, as a migration runner
// orders versions. A version longer than a stamp is left out: no 14-digit
// stamp can follow it.
func latestStamp(stamps []string) string {
	var latest uint64
	for _, s := range stamps {
		if len(s) > len(stampLayout) {
			continue
		}
		if n, err := strconv.ParseUint(s, 10, 64); err == nil && n > latest {
			latest = n
		}
	}
	return fmt.Sprintf("%014d", latest)
}

// nextStamp is the stamp after stamp: one second later when it reads as a
// time, else the next number.
func nextStamp(stamp string) string {
	if t, err := time.Parse(stampLayout, stamp); err == nil {
		return t.Add(time.Second).Format(stampLayout)
	}
	n, _ := strconv.ParseUint(stamp, 10, 64)
	return fmt.Sprintf("%014d", n+1)
}
