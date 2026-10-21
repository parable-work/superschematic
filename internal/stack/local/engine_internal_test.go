package local

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// stageFile writes content into a file of its own under t's temporary
// directory, as cargo writes a member's build, and returns its path.
func stageFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "libbuilt.a")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// digests lists the digest directories in dir.
func digests(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	return out
}

// TestStageKeepsWhatAProcessMayName: stack run stages without pruning, so
// it removes nothing stack dev's processes name; stack dev's rollout prunes
// every digest but its own and the one used last before it, which a job
// it scheduled or a stack run may still name.
func TestStageKeepsWhatAProcessMayName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "engine", "shop-db", "napi")
	staged := map[string]string{}
	for i, content := range []string{"v1", "v2", "v3"} {
		path, err := stage(stageFile(t, content), dir, "shop-db-napi.node", false)
		if err != nil {
			t.Fatal(err)
		}
		staged[content] = filepath.Base(filepath.Dir(path))
		// Each later use is later by the clock pruneStaged reads.
		then := time.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(filepath.Dir(path), then, then); err != nil {
			t.Fatal(err)
		}
	}
	if got := digests(t, dir); len(got) != 3 {
		t.Fatalf("stack run's stages left %v, want all three", got)
	}
	path, err := stage(stageFile(t, "v4"), dir, "shop-db-napi.node", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Base(filepath.Dir(path)), staged["v3"]}
	slices.Sort(want)
	if got := digests(t, dir); !slices.Equal(got, want) {
		t.Errorf("stack dev's prune left %v, want its own and the one used last, v3's: %v", got, want)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "v4" {
		t.Errorf("the staged addon is %q (%v)", data, err)
	}
	// Staging v3 again marks it used now and keeps the bytes it has.
	again, err := stage(stageFile(t, "v3"), dir, "shop-db-napi.node", true)
	if err != nil || filepath.Base(filepath.Dir(again)) != staged["v3"] {
		t.Fatalf("v3 staged again at %s (%v), want its digest %s", again, err, staged["v3"])
	}
	if got := digests(t, dir); len(got) != 2 {
		t.Errorf("staging v3 again left %v, want v3's and v4's", got)
	}
}

// TestStageAtOnce: processes that stage one member at once, some pruning
// and some not, each wait for the others under the member's lock: every
// stage succeeds, its file holds its bytes when it returns, and no copy of
// one is left half-written.
func TestStageAtOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "engine", "shop-db", "ffi")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		content := fmt.Sprintf("archive %d", i%4)
		built := stageFile(t, content)
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := stage(built, dir, engineArchive, i%2 == 0)
			if err != nil {
				errs <- err
				return
			}
			// A path a later prune removed is one a newer stage replaced.
			if data, err := os.ReadFile(path); err == nil && string(data) != content {
				errs <- fmt.Errorf("%s holds %q, want %q", path, data, content)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(filepath.Base(path), ".stage-") {
			t.Errorf("a half-written copy is left: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
