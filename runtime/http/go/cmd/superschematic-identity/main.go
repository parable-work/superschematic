// Command superschematic-identity writes a database's users through the
// identity runtime of the core user model (D50). Its one command creates a
// database's first administrator from the identity descriptor the build
// writes; superschematic identity bootstrap runs it, and a deploy job runs it
// without the compiler. runtime/http/go/README.md describes it.
//
//	superschematic-identity bootstrap --descriptor identity/<schema>.json --login LOGIN --permission P... [--name NAME] [--role admin] [--config identity.json] [--database-url URL] [--dialect postgres|sqlite]
//	superschematic-identity version
//
// The password is read from standard input, never from a flag or the
// environment. The binary holds the database drivers, so none enters the
// compiler's module graph (D27).
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
)

const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	programName = "superschematic-identity"
)

const usage = `usage:
  superschematic-identity bootstrap --descriptor identity/<schema>.json --login LOGIN --permission P... [--name NAME] [--role admin] [--config identity.json] [--database-url URL] [--dialect postgres|sqlite]
  superschematic-identity version

bootstrap creates a database's first administrator in one transaction: a
role with each --permission, a user who signs in with --login and the
password read from standard input, and the grant of the role to the user.
It refuses when any role grant exists, so it runs once per database. The
tables are those the identity descriptor names, the
identity/<schema>.json the DB build writes.

The password comes from standard input: at a terminal it is asked for
twice with the echo off; otherwise it is the first line. It must be an
Auth.Password, 8 to 128 characters, and is hashed with argon2id at the
cost --config's identity config sets, else at the runtime's default.

--database-url defaults to $DATABASE_URL. A postgres:// or postgresql://
URL selects Postgres; a sqlite: URL, a file: URI or a path selects SQLite.
--dialect overrides the URL's.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// options carry what every command shares.
type options struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	// terminal returns the prompter over stdin when it is a terminal, else
	// nil. Tests replace it.
	terminal func(stdin io.Reader, prompts io.Writer) prompter
}

// run runs one command and returns its exit code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return runWith(ctx, args, options{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv, terminal: terminalPrompter})
}

func runWith(ctx context.Context, args []string, o options) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(o.stderr, usage)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "bootstrap":
		err = bootstrap(ctx, args[1:], o)
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

// repeated is a flag that may be given more than once, each value kept.
type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }

func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}
