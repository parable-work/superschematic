package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// The static archives of the release outside a container (D47, amended).
// A Go build of the code a release generates, in a project whose naming
// file names no superscalar checkout ([paths] scalar_go), links
// superscalar's archive, the version graph's and the engine's through cgo,
// and no module the module proxy serves carries them. The generated CI's
// check job and a server's Dockerfile download the release's tarball and
// point CGO_LDFLAGS at it; stack dev and stack run do the same for the go
// builds the local provisioner runs, and `superschematic env` prints the
// variable for a go build run by hand.

// currentRelease is the running binary's release. Tests stand in a
// release built by its workflow.
var currentRelease = release.Current

// hostArchives fetches release r's static archives for this machine's
// platform into the user's cache, from the release's page or the mirror
// release.MirrorEnv names, and returns their directory.
func hostArchives(ctx context.Context, r release.Release) (string, string, error) {
	platform := release.Platform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		return "", "", fmt.Errorf("a release ships no static archives for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	cache, err := release.ArchivesCacheDir()
	if err != nil {
		return "", platform, err
	}
	dir, err := r.FetchArchives(ctx, platform, cache, r.ArchivesBase())
	return dir, platform, err
}

// buildsGo reports whether the local provisioner runs go build for env: it
// has a Go server, job or worker.
func buildsGo(env *ir.ResolvedEnvironment) bool {
	for _, d := range env.Deployables {
		switch d.Kind {
		case ir.DeployableServer, ir.DeployableJob, ir.DeployableWorker:
			if d.Language == registry.APILanguageGo {
				return true
			}
		}
	}
	return false
}

// releaseBuildEnv is the BuildEnv stack dev and stack run hand the local
// provisioner for env: CGO_LDFLAGS pointing cgo's linker at the release's
// static archives, after any CGO_LDFLAGS of the caller's, when env builds
// Go and the project names no superscalar checkout. It has nothing to set
// for a project that names one, whose binding cgo finds the archive
// beside, for a naming file that renames superscalar's Go module, which the
// release does not pin, and for a binary built from a checkout, which pins
// nothing either. When it cannot fetch them while CGO_LDFLAGS is set, it
// says so and leaves the caller's flags alone.
func releaseBuildEnv(ctx context.Context, names naming.Naming, env *ir.ResolvedEnvironment, out io.Writer) (map[string]string, error) {
	if !buildsGo(env) || names.Paths.ScalarGo != "" || names.OrDefault().ScalarGoModule != release.ScalarGoModule {
		return nil, nil
	}
	r := currentRelease()
	if r.Version == "" {
		return nil, nil
	}
	before := os.Getenv(release.CGOLinkerFlagsEnv)
	dir, platform, err := hostArchives(ctx, r)
	if err != nil {
		if before != "" {
			_, _ = fmt.Fprintf(out, "The static archives of superschematic %s: %v; the Go builds take CGO_LDFLAGS as it is set\n", r.Version, err)
			return nil, nil
		}
		return nil, fmt.Errorf("the Go servers, jobs and workers of environment %s link the static archives of superschematic %s, since the naming file's [paths] scalar_go names no superscalar checkout: %w; set CGO_LDFLAGS to -L and a directory that holds them, or [paths] scalar_go to a superscalar checkout (docs/stack-model.md, section 8.3)", env.Environment, r.Version, err)
	}
	_, _ = fmt.Fprintf(out, "The Go builds link the static archives of superschematic %s for %s, in %s\n", r.Version, platform, dir)
	return map[string]string{release.CGOLinkerFlagsEnv: release.LinkerFlags(dir, before)}, nil
}

// envVariables are the variables `superschematic env` prints, in order.
var envVariables = []string{release.CGOLinkerFlagsEnv}

func newEnvCmd(*app) *cobra.Command {
	return &cobra.Command{
		Use:   "env [<variable>...]",
		Short: "Print the CGO_LDFLAGS a go build of the generated code needs",
		Long: `Env prints the variables a go build of the code this release of
superschematic generates needs on this machine, in a project whose naming
file names no superscalar checkout ([paths] scalar_go). Such code links
superscalar's static archive, the version graph's and the engine's through
cgo, and no module the module proxy serves carries them. Env downloads the
release's archives for this machine's platform once, checks them against
the digest this binary names, keeps them in the user's cache directory, and
prints CGO_LDFLAGS pointing cgo's linker at them, as a line a POSIX shell
evaluates. stack dev and stack run do the same for the builds they run.

With variable names, env prints each one's value alone, a line each, as go
env does. The assets download from the release's page unless
SUPERSCHEMATIC_RELEASE names a mirror. A binary built from a checkout, or
not by the release workflow, names no archives, and env fails.

Examples:
  eval "$(superschematic env)" && go build ./...
  go env -w CGO_LDFLAGS="$(superschematic env CGO_LDFLAGS)"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range args {
				if !slices.Contains(envVariables, name) {
					return fmt.Errorf("env prints %s, not %s", strings.Join(envVariables, ", "), name)
				}
			}
			r := currentRelease()
			dir, platform, err := hostArchives(cmd.Context(), r)
			if err != nil {
				var hint string
				if r.Version == "" {
					hint = "; a project built with it names checkouts in its naming file's [paths], whose make setup builds the archives"
				}
				return errors.New("the static archives: " + err.Error() + hint)
			}
			values := map[string]string{release.CGOLinkerFlagsEnv: release.LinkerFlags(dir, "")}
			w := cmd.OutOrStdout()
			if len(args) == 0 {
				_, _ = fmt.Fprintf(w, "# The static archives of superschematic %s for %s\n", r.Version, platform)
				for _, name := range envVariables {
					_, _ = fmt.Fprintf(w, "export %s=%s\n", name, shellQuote(values[name]))
				}
				return nil
			}
			for _, name := range args {
				_, _ = fmt.Fprintln(w, values[name])
			}
			return nil
		},
	}
}

// shellQuote quotes s for a POSIX shell, in single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
