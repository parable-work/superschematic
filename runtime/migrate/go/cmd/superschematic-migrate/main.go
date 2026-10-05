// Command superschematic-migrate applies a migration plan that
// `superschematic migrate plan` wrote, and reads and records the state of a
// database's schema. runtime/migrate/README.md describes its commands.
//
//	superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]
//	superschematic-migrate status --service NAME [--model] [--database-url URL]
//	superschematic-migrate adopt --model model.json [--database-url URL]
//	superschematic-migrate version
//
// Exit codes: 0 done, 1 refused or failed, 2 usage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	programName = "superschematic-migrate"
)

const usage = `usage:
  superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]
  superschematic-migrate status --service NAME [--model] [--database-url URL]
  superschematic-migrate adopt --model model.json [--database-url URL]
  superschematic-migrate version

--database-url defaults to $DATABASE_URL. A postgres:// or postgresql://
URL selects Postgres; a sqlite: URL, a file: URI or a path selects SQLite.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// options carry what every command shares.
type options struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	// waits and sleep replace the runner's retry pauses in tests.
	waits []time.Duration
	sleep func(context.Context, time.Duration) error
}

// run runs one command and returns its exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return runWith(ctx, args, options{stdout: stdout, stderr: stderr, getenv: getenv})
}

func runWith(ctx context.Context, args []string, o options) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(o.stderr, usage)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "apply":
		err = apply(ctx, args[1:], o)
	case "status":
		err = status(ctx, args[1:], o)
	case "adopt":
		err = adopt(ctx, args[1:], o)
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(o.stdout, usage)
		return exitOK
	case "version", "-version", "--version":
		info, _ := debug.ReadBuildInfo()
		_, _ = fmt.Fprintf(o.stdout, "%s %s\n", programName, binaryVersion(version, info))
		return exitOK
	default:
		err = usageErrorf("unknown command %q", args[0])
	}
	var usageErr usageError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &usageErr):
		_, _ = fmt.Fprintf(o.stderr, "%s: %v\n\n%s", programName, err, usage)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(o.stderr, "%s: %v\n", programName, err)
		return exitFailed
	}
}

// version is the release version. A release build stamps it with
// -ldflags "-X main.version=X.Y.Z".
var version string

// binaryVersion returns the version the binary reports: the stamped one,
// else the module version go install recorded, else (devel).
func binaryVersion(stamped string, info *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "(devel)"
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return usageError{msg: fmt.Sprintf(format, args...)}
}

// flags returns a flag set for command whose errors are usage errors.
func flags(command string, o options) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(programName+" "+command, flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	databaseURL := fs.String("database-url", "", "the database `URL`; defaults to $DATABASE_URL")
	return fs, databaseURL
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageErrorf("%v", err)
	}
	if fs.NArg() > 0 {
		return usageErrorf("%s takes no arguments; got %q", fs.Name(), fs.Args())
	}
	return nil
}

func databaseURL(flagValue string, o options) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if url := o.getenv("DATABASE_URL"); url != "" {
		return url, nil
	}
	return "", usageErrorf("no database: pass --database-url or set DATABASE_URL")
}

// openDriver connects to the database url names.
func openDriver(ctx context.Context, url string) (migrate.Driver, func(), error) {
	switch migrate.URLDialect(url) {
	case migrate.Postgres:
		d, err := postgres.Open(ctx, url, postgres.Options{})
		if err != nil {
			return nil, nil, err
		}
		return d, func() { _ = d.Close(context.WithoutCancel(ctx)) }, nil
	default:
		d, err := sqlite.Open(ctx, url, sqlite.Options{})
		if err != nil {
			return nil, nil, err
		}
		return d, func() { _ = d.Close(context.WithoutCancel(ctx)) }, nil
	}
}

func (o options) runner(driver migrate.Driver) *migrate.Runner {
	return &migrate.Runner{Driver: driver, Log: o.stdout, Waits: o.waits, Sleep: o.sleep}
}

func apply(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("apply", o)
	planPath := fs.String("plan", "", "the plan `file` superschematic migrate plan wrote")
	phase := fs.String("phase", "all", "the steps to run: expand, contract or all")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *planPath == "" {
		return usageErrorf("apply needs --plan")
	}
	switch migrate.Phase(*phase) {
	case migrate.Expand, migrate.Contract, migrate.All:
	default:
		return usageErrorf("--phase is expand, contract or all; got %q", *phase)
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	doc, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	plan, err := migrate.ReadPlan(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", *planPath, err)
	}
	if dialect := migrate.URLDialect(url); dialect != plan.Dialect {
		return fmt.Errorf("%s: the plan is for %s and the database URL selects %s", *planPath, plan.Dialect, dialect)
	}
	driver, closeDriver, err := openDriver(ctx, url)
	if err != nil {
		return err
	}
	defer closeDriver()
	_, err = o.runner(driver).Apply(ctx, plan, migrate.Phase(*phase))
	return err
}

func status(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("status", o)
	service := fs.String("service", "", "the DB service `name`")
	printModel := fs.Bool("model", false, "print only the applied model's canonical JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *service == "" {
		return usageErrorf("status needs --service")
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	driver, closeDriver, err := openDriver(ctx, url)
	if err != nil {
		return err
	}
	defer closeDriver()
	st, err := o.runner(driver).Status(ctx, *service)
	if err != nil {
		return err
	}
	if *printModel {
		if st.ModelHash == "" || st.Model == nil {
			return fmt.Errorf("service %s has no applied model", *service)
		}
		_, err := fmt.Fprintf(o.stdout, "%s\n", st.Model)
		return err
	}
	w := o.stdout
	if !st.Recorded {
		_, err := fmt.Fprintf(w, "service %s: no state recorded: no model applied and no plan in progress\n", *service)
		return err
	}
	model, plan := "none", "none"
	if st.ModelHash != "" {
		model = st.ModelHash
	}
	if st.PlanHash != "" {
		switch st.PlanPhase {
		case migrate.PlanPhaseExpanded:
			plan = st.PlanHash + " (expand done; contract pending; the applied model is the plan's expanded model, and a plan from it supersedes the contract)"
		case migrate.PlanPhaseExpand:
			plan = st.PlanHash + " (expand done; contract pending)"
		default:
			plan = st.PlanHash + " (expand steps not all done)"
		}
	}
	_, _ = fmt.Fprintf(w, "service:          %s\n", st.Service)
	_, _ = fmt.Fprintf(w, "dialect:          %s\n", st.Dialect)
	_, _ = fmt.Fprintf(w, "applied model:    %s\n", model)
	_, _ = fmt.Fprintf(w, "plan in progress: %s\n", plan)
	_, _ = fmt.Fprintf(w, "updated at:       %s\n", st.UpdatedAt)
	if st.PlanHash != "" {
		_, _ = fmt.Fprintf(w, "steps logged:     %d\n", len(st.Steps))
		for _, step := range st.Steps {
			finished := "not finished"
			if step.FinishedAt != "" {
				finished = "finished " + step.FinishedAt
			}
			_, _ = fmt.Fprintf(w, "  %d %s %s: started %s, %s\n", step.Step, step.Phase, step.Subject, step.StartedAt, finished)
		}
	}
	return nil
}

func adopt(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("adopt", o)
	modelPath := fs.String("model", "", "the model `file` superschematic migrate plan --print-model wrote")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *modelPath == "" {
		return usageErrorf("adopt needs --model")
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	doc, err := os.ReadFile(*modelPath)
	if err != nil {
		return err
	}
	model, err := migrate.ReadModel(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", *modelPath, err)
	}
	if dialect := migrate.URLDialect(url); dialect != model.Dialect {
		return fmt.Errorf("%s: the model is for %s and the database URL selects %s", *modelPath, model.Dialect, dialect)
	}
	driver, closeDriver, err := openDriver(ctx, url)
	if err != nil {
		return err
	}
	defer closeDriver()
	replaced, err := o.runner(driver).Adopt(ctx, model)
	if err != nil {
		return err
	}
	if replaced == "" {
		replaced = "none"
	}
	_, err = fmt.Fprintf(o.stdout, "adopted model %s for service %s; it replaces %s\n", model.Hash, model.Service, replaced)
	return err
}
