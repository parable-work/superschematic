package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildcache"
	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/queryverify"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	ir "github.com/parable-work/superschematic/ir"
)

// checkFlags holds one check command's flag values.
type checkFlags struct {
	json       bool
	watch      bool
	interval   time.Duration
	namingPath string
}

// newCheckCmd is the fast half of the build loop (D72): it loads and
// verifies schema services as a build does and runs no generator, so an
// edit's verdict lands in tens of milliseconds.
func newCheckCmd(a *app) *cobra.Command {
	flags := &checkFlags{}
	cmd := &cobra.Command{
		Use:   "check [<service-dir>|<services-root>]",
		Short: "Load and verify schema services without generating code",
		Long: `Check loads schema services through the loader every build command uses
and reports what it finds, without running a generator: the TypeScript
compiler's diagnostics, the data forms' schema checks, the config rules,
the registry's decorator and kind checks and the verification pass, with
the file, line, column and severity of each. It writes nothing under the
output root. Like a build, it writes each service's sentinel
(src/service.generated.ts), the handle other services' configs import.

Given a service directory, it checks that service and every service it
depends on; given a services root, or a schemas root holding services/,
every service under it. Without an argument it checks the working
directory when it is a service, else ./schemas/services when there is one,
else the working directory as a services root.

A service whose load fails only because a service it reads failed is
reported as blocked by that service, not with its errors again.

--json writes one JSON document: ok, the services checked, each service's
status (ok, error or blocked) and every diagnostic with its service, file,
line, column, severity and message. The command exits 1 when any
diagnostic is an error.

--watch checks once, then polls the schema tree and, on each edit,
re-checks the services the edited files belong to and every service that
depends on them, and prints each result as it lands; with --json, one
document per line. It runs until interrupted and never exits on errors.

Check covers what a load covers. A few mistakes only a generator sees,
such as a worker whose queue is no @queue class of the database its API
connects to, still surface in build.

Examples:
  superschematic check
  superschematic check ./schemas/services/shop-api --json
  superschematic check --watch ./schemas/services`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return runCheck(cmd, a, flags, path)
		},
	}
	cmd.Flags().BoolVar(&flags.json, "json", false, "write the result as one JSON document (with --watch, one per line)")
	cmd.Flags().BoolVar(&flags.watch, "watch", false, "check, then re-check the services each edit affects until interrupted")
	cmd.Flags().DurationVar(&flags.interval, "interval", 200*time.Millisecond, "with --watch, how often to look for edits")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <services-root>/../superschematic.toml)")
	return cmd
}

func runCheck(cmd *cobra.Command, a *app, flags *checkFlags, path string) error {
	if flags.watch && flags.interval <= 0 {
		return fmt.Errorf("--interval %s: look for edits at some interval", flags.interval)
	}
	c, err := newChecker(a, flags.namingPath, path)
	if err != nil {
		return err
	}
	if flags.watch {
		return c.watch(cmd, flags)
	}
	report := c.runSettled(nil)
	if err := writeCheckReport(cmd.OutOrStdout(), report, flags.json, false); err != nil {
		return err
	}
	if !report.OK {
		return &ExitError{Code: 1, Err: fmt.Errorf("check found %s", countNoun(report.Errors, "error"))}
	}
	return nil
}

// resolveCheckTarget returns the services root path names and, when path
// is a service directory, that service's directory. An empty path is the
// working directory, or ./schemas/services when the working directory is
// neither a service nor a schemas root and that directory exists.
func resolveCheckTarget(path string) (servicesRoot, service string, err error) {
	if path == "" {
		path = "."
		if !isServiceDir(path) && !isDir("services") && isDir(filepath.Join("schemas", "services")) {
			path = filepath.Join("schemas", "services")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolving %s: %w", path, err)
	}
	switch {
	case !isDir(abs):
		return "", "", fmt.Errorf("directory not found: %s", path)
	case isServiceDir(abs):
		return filepath.Dir(abs), abs, nil
	case isDir(filepath.Join(abs, "services")):
		return filepath.Join(abs, "services"), "", nil
	}
	return abs, "", nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isServiceDir reports whether dir holds a schema.config in any form.
func isServiceDir(dir string) bool {
	for _, name := range []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// checkDiagnostic is one finding of a check. File is absolute until a
// report writes it, relative to the working directory when it is under it.
type checkDiagnostic struct {
	Service  string `json:"service,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (d checkDiagnostic) key() string {
	return d.File + "\x00" + strconv.Itoa(d.Line) + "\x00" + strconv.Itoa(d.Column) + "\x00" + d.Severity + "\x00" + d.Message
}

// String formats the diagnostic as compilers do, file:line:col: severity:
// message, with the service in place of a file the finding has none of.
func (d checkDiagnostic) String() string {
	var where string
	switch {
	case d.File != "" && d.Line > 0:
		where = fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column)
	case d.File != "":
		where = d.File
	case d.Service != "":
		where = d.Service
	}
	if where == "" {
		return d.Severity + ": " + d.Message
	}
	return where + ": " + d.Severity + ": " + d.Message
}

// dependencyFailedError is what a check's dependency loader returns for a
// service whose own check failed: the load that asked for it is blocked,
// and the dependency's diagnostics say why.
type dependencyFailedError struct {
	name string
}

func (e *dependencyFailedError) Error() string {
	return fmt.Sprintf("schema service %s has errors", e.name)
}

// locatedMessage matches an error message that starts with a schema file's
// path, as the data-form readers and wrapped compiler errors write it:
// "src/x.schema.json: ..." or "/abs/src/x.schema.ts:4:16: ...".
var locatedMessage = regexp.MustCompile(`(?s)^(.+?\.(?:ts|json|yaml|yml))(?::(\d+):(\d+))?: (.*)$`)

// diagnosticsOf splits a load's error into its findings. The located errors
// the loader forms (tsreader.SchemaError and verify.Diagnostic) keep their
// location, a dependency's failure is recorded in blockedBy instead, and
// any other error is one finding, located when its message starts with a
// schema file's path. A relative path is relative to serviceDir, the
// directory of the service whose load failed.
func diagnosticsOf(err error, serviceDir string, blockedBy map[string]bool) []checkDiagnostic {
	var out []checkDiagnostic
	add := func(file string, line, col int, msg string) {
		if file != "" && !filepath.IsAbs(file) {
			file = filepath.Join(serviceDir, filepath.FromSlash(file))
		}
		out = append(out, checkDiagnostic{File: file, Line: line, Column: col, Severity: string(verify.SeverityError), Message: msg})
	}
	var walk func(error)
	walk = func(err error) {
		switch e := err.(type) {
		case nil:
			return
		case *dependencyFailedError:
			blockedBy[e.name] = true
			return
		case *tsreader.SchemaError:
			add(e.File, e.Line, e.Col, e.Msg)
			return
		case tsreader.SchemaErrorList:
			for _, item := range e {
				walk(item)
			}
			return
		case *verify.Diagnostic:
			add(e.File, e.Line, e.Col, e.Msg)
			return
		case interface{ Unwrap() []error }:
			for _, item := range e.Unwrap() {
				walk(item)
			}
			return
		}
		// A wrapping error adds context to a finding that has its own
		// location, which is what an editor needs; one that wraps none
		// keeps its whole message.
		if inner := errors.Unwrap(err); inner != nil && carriesLocation(inner) {
			walk(inner)
			return
		}
		msg := err.Error()
		if m := locatedMessage.FindStringSubmatch(msg); m != nil {
			line, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			add(m[1], line, col, m[4])
			return
		}
		add("", 0, 0, msg)
	}
	walk(err)
	return out
}

// carriesLocation reports whether err holds a finding diagnosticsOf
// reads a location or a blocking dependency from.
func carriesLocation(err error) bool {
	var schemaErr *tsreader.SchemaError
	var schemaErrs tsreader.SchemaErrorList
	var diagnostic *verify.Diagnostic
	var dependency *dependencyFailedError
	return errors.As(err, &schemaErr) || errors.As(err, &schemaErrs) || errors.As(err, &diagnostic) || errors.As(err, &dependency)
}

// serviceCheck is what the last check of one service found.
type serviceCheck struct {
	// failed is set when the service's load failed.
	failed      bool
	blockedBy   map[string]bool
	diagnostics []checkDiagnostic
	seen        map[string]bool
}

func newServiceCheck() *serviceCheck {
	return &serviceCheck{blockedBy: map[string]bool{}, seen: map[string]bool{}}
}

func (s *serviceCheck) add(d checkDiagnostic) {
	if s.seen[d.key()] {
		return
	}
	s.seen[d.key()] = true
	s.diagnostics = append(s.diagnostics, d)
}

func (s *serviceCheck) errors() int {
	n := 0
	for _, d := range s.diagnostics {
		if d.Severity == string(verify.SeverityError) {
			n++
		}
	}
	return n
}

// checker checks the services of one services root and keeps what each
// check found, so a watch re-checks only the services an edit can change.
type checker struct {
	app          *app
	namingPath   string
	servicesRoot string
	schemasRoot  string
	// target is the directory of the one service the command names, else
	// empty: the whole services root.
	target string

	names    naming.Naming
	reg      *registry.Registry
	services []buildplan.Service
	// schemas holds each service whose last load succeeded.
	schemas map[string]*ir.Schema
	results map[string]*serviceCheck
	// general holds the findings no service owns: the naming file's,
	// discovery's.
	general []checkDiagnostic
	// rewrote lists the services whose sentinel the last run rewrote.
	rewrote []string
}

func newChecker(a *app, namingPath, path string) (*checker, error) {
	servicesRoot, target, err := resolveCheckTarget(path)
	if err != nil {
		return nil, err
	}
	return &checker{
		app:          a,
		namingPath:   namingPath,
		servicesRoot: servicesRoot,
		schemasRoot:  filepath.Dir(servicesRoot),
		target:       target,
		schemas:      map[string]*ir.Schema{},
		results:      map[string]*serviceCheck{},
	}, nil
}

// checkReport is one check's result, as --json writes it.
type checkReport struct {
	OK           bool   `json:"ok"`
	ServicesRoot string `json:"servicesRoot"`
	// Changed lists the files whose edit started a watch's re-check.
	Changed []string `json:"changed,omitempty"`
	// Checked names the services this run loaded; a watch keeps the other
	// services' last results.
	Checked     []string             `json:"checked"`
	Errors      int                  `json:"errors"`
	Warnings    int                  `json:"warnings"`
	DurationMS  int64                `json:"durationMs"`
	Services    []checkServiceReport `json:"services"`
	Diagnostics []checkDiagnostic    `json:"diagnostics"`
}

// checkServiceReport is one service's status in a report.
type checkServiceReport struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Dir       string   `json:"dir"`
	Status    string   `json:"status"`
	BlockedBy []string `json:"blockedBy,omitempty"`
}

// run checks the services in scope that changed names and every service
// that depends on one of them, or every service in scope when changed is
// nil, and reports all of scope: the services it did not check keep their
// last result. A run of every service reads the naming file and discovers
// the services again; a run of some reuses what the last one discovered,
// since a watch asks for some only when no config or naming file changed.
func (c *checker) run(changed []string) checkReport {
	start := time.Now()
	c.general = nil
	c.rewrote = nil
	report := checkReport{ServicesRoot: diagnosticPath(c.servicesRoot), Checked: []string{}, Services: []checkServiceReport{}, Diagnostics: []checkDiagnostic{}}
	finish := func(scope []buildplan.Service) checkReport {
		report.DurationMS = time.Since(start).Milliseconds()
		c.fill(&report, scope)
		return report
	}

	if changed == nil || c.services == nil {
		changed = nil
		if err := c.discover(); err != nil {
			c.general = diagnosticsOf(err, c.schemasRoot, map[string]bool{})
			c.services = nil
			c.schemas = map[string]*ir.Schema{}
			c.results = map[string]*serviceCheck{}
			return finish(nil)
		}
	}
	scope := c.services
	if c.target != "" {
		root := ""
		for _, service := range c.services {
			if filepath.Clean(service.Dir) == c.target {
				root = service.Name
			}
		}
		closure, err := buildplan.Closure(c.services, root)
		if err != nil {
			c.general = diagnosticsOf(err, c.target, map[string]bool{})
			return finish(nil)
		}
		scope = closure
	}
	toCheck := scope
	if changed != nil {
		inScope := make(map[string]bool, len(scope))
		for _, service := range scope {
			inScope[service.Name] = true
		}
		toCheck = nil
		for _, service := range buildplan.Dependents(c.services, changed, c.referencesOf) {
			if inScope[service.Name] {
				toCheck = append(toCheck, service)
			}
		}
	}

	discovered := make(map[string]bool, len(c.services))
	for _, service := range c.services {
		discovered[service.Name] = true
	}
	for name := range c.results {
		if !discovered[name] {
			delete(c.results, name)
			delete(c.schemas, name)
		}
	}
	for _, service := range toCheck {
		delete(c.schemas, service.Name)
		c.results[service.Name] = newServiceCheck()
	}

	var programs *tsreader.ProgramCache
	if dirs, err := tsServiceDirectories(toCheck); err != nil {
		c.general = append(c.general, diagnosticsOf(err, c.servicesRoot, map[string]bool{})...)
	} else if len(dirs) > 0 {
		programs = tsreader.NewProgramCache(dirs)
		defer programs.Close()
	}
	// The catalog is build-all's: a reference to another service resolves
	// against every discovered one.
	catalog := make(map[string]registry.SchemaCatalogEntry, len(c.services))
	for _, service := range c.services {
		catalog[service.Name] = registry.SchemaCatalogEntry{Kind: string(service.Config.Kind), AuthDB: service.Config.AuthDB}
	}
	load := &checkLoad{checker: c, catalog: catalog, programs: programs, loading: map[string]bool{}, done: map[string]bool{}}
	for _, service := range toCheck {
		load.check(service)
	}
	report.Checked = load.checked
	return finish(scope)
}

// discover reads the naming file and the registry, writes every sentinel
// a config may import (D34), and discovers the services, as build-all does
// before it loads any.
func (c *checker) discover() error {
	buildcache.SetSchemasRoot(c.schemasRoot)
	names, err := resolveNaming(c.namingPath, c.schemasRoot)
	if err != nil {
		return err
	}
	reg, err := c.app.resolveRegistry(names)
	if err != nil {
		return err
	}
	c.names, c.reg = names, reg
	if err := buildplan.EnsureSentinels(c.servicesRoot, reg, nil); err != nil {
		return err
	}
	services, err := buildplan.DiscoverWith(c.servicesRoot, filepath.Join(c.schemasRoot, "dist"), reg)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return fmt.Errorf("no schema services found under %s", c.servicesRoot)
	}
	c.services = services
	return nil
}

// referencesOf lists the services the last clean load of service found
// named by its decorator arguments (D41), such as the servers of a stack:
// edges no config states, which a watch follows as it follows the
// config's.
func (c *checker) referencesOf(service buildplan.Service) []string {
	schema, ok := c.schemas[service.Name]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(schema.References))
	for _, ref := range schema.References {
		names = append(names, ref.Name)
	}
	return names
}

// ownerOf returns the service whose directory holds file, or "".
func (c *checker) ownerOf(file string) string {
	for _, service := range c.services {
		if strings.HasPrefix(file, service.Dir+string(filepath.Separator)) {
			return service.Name
		}
	}
	return ""
}

// fill writes the status of every service in scope and every finding into
// report, in build order.
func (c *checker) fill(report *checkReport, scope []buildplan.Service) {
	add := func(d checkDiagnostic) {
		d.File = diagnosticPath(d.File)
		if d.Severity == string(verify.SeverityError) {
			report.Errors++
		} else {
			report.Warnings++
		}
		report.Diagnostics = append(report.Diagnostics, d)
	}
	for _, d := range c.general {
		add(d)
	}
	for _, service := range scope {
		entry := checkServiceReport{Name: service.Name, Kind: string(service.Config.Kind), Dir: diagnosticPath(service.Dir), Status: "ok"}
		result := c.results[service.Name]
		if result == nil {
			result = newServiceCheck()
		}
		switch {
		case result.errors() > 0:
			entry.Status = "error"
		case result.failed:
			entry.Status = "blocked"
			for _, dep := range sortedNames(result.blockedBy) {
				if dep != service.Name {
					entry.BlockedBy = append(entry.BlockedBy, dep)
				}
			}
		}
		report.Services = append(report.Services, entry)
		for _, d := range result.diagnostics {
			add(d)
		}
	}
	report.OK = report.Errors == 0
}

// checkLoad is one run's loads: each service in it loads once, its
// dependencies first, through the run's shared TypeScript program.
type checkLoad struct {
	checker  *checker
	catalog  map[string]registry.SchemaCatalogEntry
	programs *tsreader.ProgramCache
	loading  map[string]bool
	done     map[string]bool
	checked  []string
}

// check loads service and records what the load found under the service
// each finding's file belongs to: a finding in a dependency's file is the
// dependency's, and the load that met it is blocked by that dependency.
func (l *checkLoad) check(service buildplan.Service) {
	c := l.checker
	if l.done[service.Name] || l.loading[service.Name] {
		return
	}
	l.loading[service.Name] = true
	defer func() {
		l.loading[service.Name] = false
		l.done[service.Name] = true
	}()
	l.checked = append(l.checked, service.Name)
	result := c.results[service.Name]
	if result == nil {
		result = newServiceCheck()
		c.results[service.Name] = result
	}

	var warnings []checkDiagnostic
	opts := []loader.Option{
		loader.WithSchemaCatalog(l.catalog),
		loader.WithNaming(c.names),
		loader.WithRegistry(c.reg),
		loader.WithDependencyLoader(l.dependency),
		loader.WithWarnings(func(d *verify.Diagnostic) {
			file := d.File
			if file != "" && !filepath.IsAbs(file) {
				file = filepath.Join(service.Dir, filepath.FromSlash(file))
			}
			warnings = append(warnings, checkDiagnostic{File: file, Line: d.Line, Column: d.Col, Severity: string(verify.SeverityWarning), Message: d.Msg})
		}),
	}
	if l.programs != nil {
		opts = append(opts, loader.WithTSProgramCache(l.programs))
	}
	schema, cfg, err := loader.LoadServiceWithConfig(service.Dir, opts...)

	findings := warnings
	if err != nil {
		result.failed = true
		findings = append(findings, diagnosticsOf(err, service.Dir, result.blockedBy)...)
	} else {
		c.schemas[service.Name] = schema
		// A DB service's queries are the ones its queries.lock verified
		// (D86).
		if err := queryverify.CheckLock(service.Dir, schema); err != nil {
			findings = append(findings, checkDiagnostic{File: filepath.Join(service.Dir, queryverify.LockFile), Severity: string(verify.SeverityError), Message: err.Error()})
		}
		// A projection's built migration is never rewritten (D102): the
		// pairs a build would write are held to projections.lock, as the
		// build holds them before it writes.
		if err := c.checkProjectionStamps(service.Dir, schema, cfg); err != nil {
			findings = append(findings, checkDiagnostic{File: filepath.Join(service.Dir, generator.ProjectionsLockFile), Severity: string(verify.SeverityError), Message: err.Error()})
		}
		if hasServiceSentinel(service.Dir, cfg, c.reg) {
			changed, err := sentinel.EmitHandle(service.Dir, cfg, c.reg, sentinel.HandleTypesOf(schema))
			if err != nil {
				findings = append(findings, checkDiagnostic{File: filepath.Join(service.Dir, "src", sentinel.GeneratedFile), Severity: string(verify.SeverityError), Message: "writing the service sentinel: " + err.Error()})
			} else if changed {
				c.rewrote = append(c.rewrote, service.Name)
			}
		}
	}
	for _, d := range findings {
		owner := service.Name
		if d.File != "" {
			if o := c.ownerOf(d.File); o != "" {
				owner = o
			}
		}
		if owner != service.Name {
			result.blockedBy[owner] = true
			if c.results[owner] == nil {
				c.results[owner] = newServiceCheck()
			}
		}
		d.Service = owner
		c.results[owner].add(d)
	}
}

// checkProjectionStamps renders the projection migrations a build of the
// DB service at dir would write and holds them to its projections.lock
// (generator.CheckProjectionStamps), with no database.
func (c *checker) checkProjectionStamps(dir string, schema *ir.Schema, cfg *schemaconfig.SchemaConfig) error {
	if schema.Kind != ir.SchemaKindDB || len(schema.Projections()) == 0 {
		return nil
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, c.reg)
	if err != nil {
		return nil
	}
	deps := map[string]*ir.Schema{}
	for _, dep := range cfg.Dependencies {
		deps[dep.Name] = c.schemas[dep.Name]
	}
	return generator.CheckProjectionStamps(dir, schema, cfg, outputs, deps, c.names)
}

// dependency is the run's dependency loader: a service already loaded
// clean is reused, one whose check failed blocks the load that reads it,
// and any other is checked first.
func (l *checkLoad) dependency(name string) (*ir.Schema, error) {
	c := l.checker
	if !l.done[name] && !l.loading[name] {
		if _, ok := c.schemas[name]; !ok {
			for _, service := range c.services {
				if service.Name == name {
					if result := c.results[name]; result == nil || !result.failed {
						l.check(service)
					}
				}
			}
		}
	}
	if schema, ok := c.schemas[name]; ok {
		return schema, nil
	}
	if result := c.results[name]; result != nil && result.failed {
		return nil, &dependencyFailedError{name: name}
	}
	if l.loading[name] {
		return nil, fmt.Errorf("schema service %s is read while it loads", name)
	}
	return nil, fmt.Errorf("schema service %s not found", name)
}

// diagnosticPath writes path relative to the working directory when it is
// under it, else as it is.
func diagnosticPath(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}

// writeCheckReport writes report as text, a line per finding and a
// summary, or as JSON: indented, or on one line for a watch, so each
// result is a line a reader can take as it lands.
func writeCheckReport(w io.Writer, report checkReport, asJSON, watching bool) error {
	if asJSON {
		var data []byte
		var err error
		if watching {
			data, err = json.Marshal(report)
		} else {
			data, err = json.MarshalIndent(report, "", "  ")
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	for _, d := range report.Diagnostics {
		if _, err := fmt.Fprintln(w, d.String()); err != nil {
			return err
		}
	}
	var blocked, failed []string
	for _, service := range report.Services {
		switch service.Status {
		case "error":
			failed = append(failed, service.Name)
		case "blocked":
			blocked = append(blocked, fmt.Sprintf("%s (by %s)", service.Name, strings.Join(service.BlockedBy, ", ")))
		}
	}
	summary := fmt.Sprintf("Checked %s in %dms", countNoun(len(report.Checked), "schema service"), report.DurationMS)
	if len(report.Checked) != len(report.Services) {
		summary = fmt.Sprintf("Checked %d of %s in %dms", len(report.Checked), countNoun(len(report.Services), "schema service"), report.DurationMS)
	}
	switch {
	case report.Errors == 0 && report.Warnings == 0:
		summary += ": ok"
	case report.Errors == 0:
		summary += ": ok, " + countNoun(report.Warnings, "warning")
	default:
		summary += ": " + countNoun(report.Errors, "error")
		if report.Warnings > 0 {
			summary += ", " + countNoun(report.Warnings, "warning")
		}
	}
	if len(failed) > 0 {
		summary += "; failed: " + strings.Join(failed, ", ")
	}
	if len(blocked) > 0 {
		summary += "; blocked: " + strings.Join(blocked, ", ")
	}
	_, err := fmt.Fprintln(w, summary)
	return err
}

// sortedNames returns the names set holds, sorted.
func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
