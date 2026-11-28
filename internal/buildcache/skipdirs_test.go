package buildcache

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	inttasks "github.com/parable-work/superschematic/internal/tasks"
)

// TestATasksSchemaInputSkipsTheDirectoriesADigestSkips: a task's schema
// input leaves out the directory names a service's digest leaves out
// (D203). A name in one list alone would be a file that changes what a
// build reuses and not what a task is keyed on, or the other way about.
func TestATasksSchemaInputSkipsTheDirectoriesADigestSkips(t *testing.T) {
	theirs := append([]string{}, inttasks.SkippedDirs...)
	sort.Strings(theirs)
	require.Equal(t, HashSkipDirs(), theirs)
}
