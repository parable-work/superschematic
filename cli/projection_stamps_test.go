package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator"
)

// TestCheckAndBuildHoldAProjectionToItsBuiltMigration is J2's acceptance
// through the commands (D102): an unbuilt stamp checks clean and the
// build records it in projections.lock; a changed doc comment, which
// changes the view's COMMENT in the up file but not the model the queries
// lock holds, fails check and build under the same stamp, naming the
// next one, while the queries lock still holds; build --rewrite-migration
// records it again; and the next stamp checks and builds.
func TestCheckAndBuildHoldAProjectionToItsBuiltMigration(t *testing.T) {
	serviceDir := prepareQueriesService(t)
	_, stderr, err := runCommand(t, "check", serviceDir)
	require.NoError(t, err, stderr)
	lockPath := filepath.Join(serviceDir, generator.ProjectionsLockFile)
	_, err = os.Stat(lockPath)
	require.True(t, os.IsNotExist(err), "check wrote %s", generator.ProjectionsLockFile)

	stdout, stderr, err := runCommand(t, "build", serviceDir)
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "recorded the migration stamps built in")
	lock, err := generator.ReadProjectionsLock(lockPath)
	require.NoError(t, err)
	require.Len(t, lock.Migrations, 1)
	assert.Equal(t, "20261027000000", lock.Migrations[0].Migration)
	assert.Equal(t, "LineListEntry", lock.Migrations[0].Projection)

	c, err := newChecker(&app{}, "", serviceDir)
	require.NoError(t, err)
	_, ok := c.snapshot()[lockPath]
	assert.True(t, ok, "check --watch stamps %s", generator.ProjectionsLockFile)

	editService(t, serviceDir, "src/line-list.projection.schema.ts", "// A line with its account's and its category's names, flat", "// A line with the names of its account and its category, flat")
	stdout, _, err = runCommand(t, "check", serviceDir)
	require.Error(t, err)
	assert.Contains(t, stdout, "projections.lock: error: projection LineListEntry changed under its migration stamp 20261027000000, which projections.lock records as built")
	assert.Contains(t, stdout, `give it a new stamp, migration: "20261027000001"`)
	assert.NotContains(t, stdout, "changed since it was verified", "a doc comment is no query to verify again")

	_, _, err = runCommand(t, "build", serviceDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--rewrite-migration 20261027000000")
	_, _, err = runCommand(t, "build", "--with-deps", "--rewrite-migration", "20261027000000", serviceDir)
	require.EqualError(t, err, "--rewrite-migration rewrites a DB service's projection migrations and cannot be combined with --with-deps; build the DB service alone")

	_, stderr, err = runCommand(t, "build", "--rewrite-migration", "20261027000000", serviceDir)
	require.NoError(t, err, stderr)
	_, stderr, err = runCommand(t, "check", serviceDir)
	require.NoError(t, err, stderr)

	editService(t, serviceDir, "src/line-list.projection.schema.ts", "its category, flat", "its category, flat and plain")
	editService(t, serviceDir, "src/line-list.projection.schema.ts", `migration: "20261027000000"`, `migration: "20261027000001"`)
	_, stderr, err = runCommand(t, "check", serviceDir)
	require.NoError(t, err, stderr)
	_, stderr, err = runCommand(t, "build", serviceDir)
	require.NoError(t, err, stderr)
	lock, err = generator.ReadProjectionsLock(lockPath)
	require.NoError(t, err)
	require.Len(t, lock.Migrations, 2)
	assert.Equal(t, []string{"20261027000000", "20261027000001"}, []string{lock.Migrations[0].Migration, lock.Migrations[1].Migration})
}
