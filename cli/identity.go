package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// IdentityEnv names the environment variable that locates the identity
// runner, superschematic-identity (runtime/http/go/README.md), when it is
// not on PATH as IdentityBinary.
const (
	IdentityEnv    = "SUPERSCHEMATIC_IDENTITY"
	IdentityBinary = "superschematic-identity"
)

// identityBootstrapFlags holds one identity bootstrap command's flag
// values.
type identityBootstrapFlags struct {
	descriptor  string
	databaseURL string
	dialect     string
	login       string
	name        string
	role        string
	permissions []string
	config      string
	namingPath  string
}

// newIdentityCmd groups the commands of the core user model (D50).
func newIdentityCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Work on the users of a database with the core user model",
		Long: `identity works on the users, roles and grants of a database whose schema
has the User trait, the core user model. Past the first administrator,
which bootstrap creates, the administration routes of @userAdministration
manage them.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newIdentityBootstrapCmd(a))
	return cmd
}

func newIdentityBootstrapCmd(a *app) *cobra.Command {
	flags := &identityBootstrapFlags{}
	cmd := &cobra.Command{
		Use:   "bootstrap [<db-service-dir>] --login <login> --permission <permission>...",
		Short: "Create a database's first administrator: a role, a user with a password, and the grant",
		Long: `bootstrap creates a database's first administrator in one transaction: a
role with the permissions --permission names, a user who signs in with
--login and a password, and the grant of the role to the user. It refuses
when any role grant exists, so it runs once per database; the
administrator then manages every other user and role through the
administration routes. No one grants what they do not hold, so give the
role every permission the administrator hands out, the administration
routes' own among them: identity covers identity.users.read and the rest,
under the default identity_permission_prefix.

The tables are those of the DB service at <db-service-dir>, whose schema
has the User and UserRole traits. It loads as build loads it, and its
identity descriptor is built in process. --descriptor names the
descriptor a build wrote instead (identity/<schema>.json in the service's
Go types module), with no service directory.

The identity runner, superschematic-identity, writes the database: it
holds the database drivers, so none enters the compiler. bootstrap runs it
with the descriptor and the other flags, hands it standard input, output
and error, and exits with its status. The runner is the binary
$SUPERSCHEMATIC_IDENTITY names, else superschematic-identity on PATH. A
deploy job runs it alone, with the descriptor the build wrote.

The database is --database-url, else $DATABASE_URL: a postgres:// or
postgresql:// URL is Postgres; a sqlite: URL, a file: URI or a path is
SQLite. --dialect overrides the URL's. The database must hold the
schema's tables: migrate it first.

The password is read from standard input, never from a flag or the
environment. At a terminal the runner asks for it twice with the echo
off; otherwise it reads one line. It must be an Auth.Password, 8 to 128
characters, and is hashed with argon2id at the cost of --config, an
identity config file, else at the runtime's default. The runner prints
the role and the user it created, never the password or its hash.

A permission is dotted segments of letters, digits, _ and -. The build
writes each API's permission catalog beside its OpenAPI document
(<schemas-root>/dist/api/<api>/permissions.json). When catalogs of APIs
whose authDb is this service are there, a --permission that is none of
their permissions and covers none draws a warning, and is granted all
the same: an engine's schemas name permissions at run time.

Examples:
  superschematic identity bootstrap ./schemas/services/shop-db --login admin@example.com --permission identity --permission orders
  printf '%s\n' "$ADMIN_PASSWORD" | superschematic identity bootstrap ./schemas/services/shop-db --database-url "$SHOP_DB_URL" --login admin@example.com --name "Shop Admin" --role owner --permission identity
  superschematic identity bootstrap --descriptor ./schemas/dist/types/go/shop-db/identity/shop-db.json --database-url sqlite:./shop.db --login admin@example.com --permission identity`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityBootstrap(cmd, a, flags, args)
		},
	}
	cmd.Flags().StringVar(&flags.descriptor, "descriptor", "", "the identity descriptor file a build wrote, in place of a service directory")
	cmd.Flags().StringVar(&flags.databaseURL, "database-url", "", "the database URL (default $DATABASE_URL)")
	cmd.Flags().StringVar(&flags.dialect, "dialect", "", "postgres or sqlite, in place of the dialect the URL selects")
	cmd.Flags().StringVar(&flags.login, "login", "", "the administrator's login, a value of the User table's login scalar (required)")
	cmd.Flags().StringVar(&flags.name, "name", "", "the administrator's display name (default the login), for a User table that names a name field")
	cmd.Flags().StringVar(&flags.role, "role", "admin", "the name of the role the administrator holds")
	cmd.Flags().StringArrayVar(&flags.permissions, "permission", nil, "a permission the role carries, such as identity or orders.read (repeatable; at least one)")
	cmd.Flags().StringVar(&flags.config, "config", "", "an identity config JSON file whose password.argon2 sets the hash's cost (default the runtime's)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <db-service-dir>/../../superschematic.toml)")
	_ = cmd.MarkFlagRequired("login")
	_ = cmd.MarkFlagRequired("permission")
	return cmd
}

func runIdentityBootstrap(cmd *cobra.Command, a *app, flags *identityBootstrapFlags, args []string) error {
	switch {
	case len(args) == 0 && flags.descriptor == "":
		return errors.New("identity bootstrap: name the DB service directory, or the identity descriptor with --descriptor")
	case len(args) == 1 && flags.descriptor != "":
		return errors.New("identity bootstrap: a service directory and --descriptor both name the tables; give one")
	}
	runner, err := identityRunner()
	if err != nil {
		return err
	}

	descriptor := flags.descriptor
	if descriptor == "" {
		schema, schemasRoot, err := loadIdentityService(cmd, a, flags, args[0])
		if err != nil {
			return err
		}
		d, ok, err := identitydesc.Describe(schema)
		if err != nil {
			return fmt.Errorf("identity bootstrap: %w", err)
		}
		if !ok {
			return fmt.Errorf("identity bootstrap: %s has no User table: give one DB table the User trait (implements User<{ login: ... }>) to turn the user model on", schema.Name)
		}
		if d.Role == nil {
			return fmt.Errorf("identity bootstrap: %s has no UserRole table, so there is no role to grant: give one DB table the UserRole trait (implements UserRole)", schema.Name)
		}
		if descriptor, err = writeDescriptor(d, schema.Name); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(descriptor)) }()
		catalogs, err := authDBCatalogs(schemasRoot, schema.Name)
		if err != nil {
			return err
		}
		warnUncatalogued(cmd.ErrOrStderr(), flags.permissions, catalogs)
	}

	run := exec.CommandContext(cmd.Context(), runner, bootstrapRunnerArgs(cmd, flags, descriptor)...)
	run.Stdin, run.Stdout, run.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := run.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return &ExitError{Code: exitErr.ExitCode(), Err: fmt.Errorf("identity bootstrap: %s exited with status %d", IdentityBinary, exitErr.ExitCode())}
		}
		return fmt.Errorf("identity bootstrap: run %s: %w", runner, err)
	}
	return nil
}

// identityRunner locates the identity runner. It runs as a binary of its
// own because it holds the database drivers, which stay out of the
// compiler's module (D27), as the migration runner does.
func identityRunner() (string, error) {
	if path := os.Getenv(IdentityEnv); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(IdentityBinary)
	if err != nil {
		return "", fmt.Errorf("identity bootstrap: %s is not on PATH, and %s names no runner; bootstrap writes the database with it: "+
			"install it from a release, or build it (cd runtime/http/go && go build -o \"$(go env GOPATH)/bin/%s\" ./cmd/%s, "+
			"with the superscalar library on CGO_LDFLAGS), as runtime/http/go/README.md says: %w", IdentityBinary, IdentityEnv, IdentityBinary, IdentityBinary, err)
	}
	return path, nil
}

// bootstrapRunnerArgs are the runner's arguments: bootstrap with the
// descriptor and every flag the command was given but --naming, which only
// the load reads. A flag left out takes the runner's default.
func bootstrapRunnerArgs(cmd *cobra.Command, flags *identityBootstrapFlags, descriptor string) []string {
	args := []string{"bootstrap", "--descriptor", descriptor}
	for _, f := range []struct{ name, value string }{
		{"database-url", flags.databaseURL},
		{"dialect", flags.dialect},
		{"login", flags.login},
		{"name", flags.name},
		{"role", flags.role},
		{"config", flags.config},
	} {
		if cmd.Flags().Changed(f.name) {
			args = append(args, "--"+f.name, f.value)
		}
	}
	for _, p := range flags.permissions {
		args = append(args, "--permission", p)
	}
	return args
}

// writeDescriptor writes d to a file in a new temporary directory, for the
// runner to read, and returns its path.
func writeDescriptor(d identitydesc.Descriptor, schema string) (string, error) {
	data, err := d.JSON()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "superschematic-identity-")
	if err != nil {
		return "", fmt.Errorf("identity bootstrap: write the descriptor: %w", err)
	}
	path := filepath.Join(dir, schema+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("identity bootstrap: write the descriptor: %w", err)
	}
	return path, nil
}

// loadIdentityService loads the DB service at servicePath as build loads
// one service, and returns it with its schemas root.
func loadIdentityService(cmd *cobra.Command, a *app, flags *identityBootstrapFlags, servicePath string) (*ir.Schema, string, error) {
	absService, err := filepath.Abs(servicePath)
	if err != nil {
		return nil, "", fmt.Errorf("resolving service path: %w", err)
	}
	if info, err := os.Stat(absService); err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("service directory not found: %s", servicePath)
	}
	schemasRoot := filepath.Dir(filepath.Dir(absService))
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return nil, "", err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return nil, "", err
	}
	if targetImportsSiblingSentinels(absService, reg) || configImportsSentinels(absService, names) {
		if err := buildplan.EnsureSentinels(filepath.Dir(absService), reg, cmd.ErrOrStderr()); err != nil {
			return nil, "", err
		}
	}
	opts := []loader.Option{loader.WithNaming(names), loader.WithRegistry(reg)}
	loadDependency := memoizedSchemas(func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(absService, "..", name), opts...)
	})
	schema, err := loader.LoadService(absService, append(opts, loader.WithDependencyLoader(loadDependency))...)
	if err != nil {
		return nil, "", err
	}
	if schema.Kind != ir.SchemaKindDB {
		return nil, "", fmt.Errorf("identity bootstrap: %s is of kind %s; bootstrap takes the DB service whose schema has the User trait", schema.Name, schema.Kind)
	}
	return schema, schemasRoot, nil
}

// authDBCatalogs reads the permission catalogs the build wrote under the
// schemas root's default output root, of the APIs whose authDb is db. A
// catalog that does not read is skipped: the catalogs inform and decide
// nothing.
func authDBCatalogs(schemasRoot, db string) ([]permcatalog.Catalog, error) {
	paths, err := filepath.Glob(filepath.Join(schemasRoot, "dist", "api", "*", permcatalog.FileName))
	if err != nil {
		return nil, err
	}
	var catalogs []permcatalog.Catalog
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		catalog, err := permcatalog.Parse(data)
		if err != nil || catalog.AuthDB != db {
			continue
		}
		catalogs = append(catalogs, catalog)
	}
	return catalogs, nil
}

// warnUncatalogued writes a warning to w for each permission that is no
// permission of the catalogs and covers none, when there are catalogs. A
// permission covers itself and those under it (orders covers orders.read),
// as the runtimes' matchers read it.
func warnUncatalogued(w io.Writer, permissions []string, catalogs []permcatalog.Catalog) {
	if len(catalogs) == 0 {
		return
	}
	var apis []string
	for _, catalog := range catalogs {
		apis = append(apis, catalog.API)
	}
	seen := map[string]bool{}
	for _, p := range permissions {
		if seen[p] {
			continue
		}
		seen[p] = true
		named := false
		for _, catalog := range catalogs {
			for _, listed := range catalog.Permissions {
				named = named || listed.Name == p || strings.HasPrefix(listed.Name, p+".")
			}
		}
		if !named {
			_, _ = fmt.Fprintf(w, "identity bootstrap: warning: --permission %s is no permission the operations of %s check, and covers none; the role carries it all the same\n",
				p, strings.Join(apis, ", "))
		}
	}
}
