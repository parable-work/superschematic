package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/queryverify"
	"github.com/parable-work/superschematic/internal/sentinel"
)

// settledRuns bounds the runs runSettled makes: each run after the first
// re-checks the dependents of a service whose sentinel the one before
// rewrote, and a sentinel a run leaves as it is ends the chain.
const settledRuns = 3

// runSettled runs a check of changed (nil for all of scope), then again
// for each service whose sentinel the run rewrote, until none is: the
// services that import a handle loaded it from the shared TypeScript
// program before its build wrote it, as a build-all's do. The report is the
// last run's, with every run's services and time.
func (c *checker) runSettled(changed []string) checkReport {
	report := c.run(changed)
	for i := 1; i < settledRuns && len(c.rewrote) > 0; i++ {
		next := c.run(c.rewrote)
		for _, name := range report.Checked {
			if !slices.Contains(next.Checked, name) {
				next.Checked = append(next.Checked, name)
			}
		}
		next.DurationMS += report.DurationMS
		next.Changed = report.Changed
		report = next
	}
	return report
}

// watch checks once, then looks for edits every interval and re-checks
// the services each edit affects, printing each result as it lands, until
// the command's context ends or the process is interrupted. Polling the
// schema tree, a few hundred small files at most, costs less than the
// check it starts, needs no file-notification dependency and sees edits a
// container's or a network file system's events miss (D72).
func (c *checker) watch(cmd *cobra.Command, flags *checkFlags) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := cmd.OutOrStdout()

	seen := c.snapshot()
	if err := writeCheckReport(w, c.runSettled(nil), flags.json, true); err != nil {
		return err
	}
	if !flags.json {
		_, _ = fmt.Fprintf(w, "Watching %s for edits (Ctrl-C to stop)\n", diagnosticPath(c.servicesRoot))
	}
	ticker := time.NewTicker(flags.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		now := c.snapshot()
		files := changedFiles(seen, now)
		seen = now
		if len(files) == 0 {
			continue
		}
		report := c.runSettled(c.servicesOf(files))
		for _, file := range files {
			report.Changed = append(report.Changed, diagnosticPath(file))
		}
		if !flags.json {
			_, _ = fmt.Fprintf(w, "\n%s changed: %s\n", time.Now().Format("15:04:05"), strings.Join(report.Changed, ", "))
		}
		if err := writeCheckReport(w, report, flags.json, true); err != nil {
			return err
		}
	}
}

// fileStamp is what a snapshot records of one file.
type fileStamp struct {
	modified time.Time
	size     int64
}

// snapshot stamps every file a check reads that an edit can change: the
// schemas root's naming file and base tsconfig, the naming file --naming
// names, and the TypeScript, JSON, YAML and TOML files of each service and
// its queries.lock (D86), outside node_modules, dist, target and hidden
// directories. A service's
// sentinel is left out, since every check may write it.
func (c *checker) snapshot() map[string]fileStamp {
	files := map[string]fileStamp{}
	stamp := func(path string) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files[path] = fileStamp{modified: info.ModTime(), size: info.Size()}
		}
	}
	stamp(filepath.Join(c.schemasRoot, "superschematic.toml"))
	stamp(filepath.Join(c.schemasRoot, "tsconfig.base.json"))
	if c.namingPath != "" {
		if abs, err := filepath.Abs(c.namingPath); err == nil {
			stamp(abs)
		}
	}
	_ = filepath.WalkDir(c.servicesRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A file removed while the walk runs is the next snapshot's.
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if path == c.servicesRoot {
				return nil
			}
			skipped := strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" || name == "target"
			if skipped || (filepath.Dir(path) == c.servicesRoot && strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if name == sentinel.GeneratedFile || strings.HasPrefix(name, ".") {
			return nil
		}
		switch filepath.Ext(name) {
		case ".ts", ".json", ".yaml", ".yml", ".toml":
			stamp(path)
		}
		// A DB service's queries are held to its lock (D86), which
		// queries verify rewrites.
		if name == queryverify.LockFile {
			stamp(path)
		}
		return nil
	})
	return files
}

// changedFiles lists the files added, removed or changed between two
// snapshots, sorted.
func changedFiles(before, after map[string]fileStamp) []string {
	var changed []string
	for path, stamp := range after {
		if old, ok := before[path]; !ok || old != stamp {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed
}

// servicesOf names the services the changed files belong to, or returns
// nil, which discovers the services again and re-checks every one, when a
// file belongs to none (the naming file, the base tsconfig, a new service)
// or is a service's config, package.json or tsconfig.json, whose edit can
// rename a service, move an edge or change how it loads.
func (c *checker) servicesOf(files []string) []string {
	var names []string
	for _, file := range files {
		base := filepath.Base(file)
		if strings.HasPrefix(base, "schema.config.") || base == "package.json" || base == "tsconfig.json" {
			return nil
		}
		owner := c.ownerOf(file)
		if owner == "" {
			return nil
		}
		if !slices.Contains(names, owner) {
			names = append(names, owner)
		}
	}
	return names
}
