package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// mine lists the roles the connection's user is, or is granted, directly
// or through other roles, read from pg_auth_members as grantees does.
const mine = `
WITH RECURSIVE mine(oid) AS (
  SELECT oid FROM pg_roles WHERE rolname = current_user
  UNION
  SELECT m.roleid FROM pg_auth_members m JOIN mine ON m.member = mine.oid
)`

// roleCreator names the role the connection's user creates a role as: the
// user itself when it may create roles, else the first role by name it is
// granted that may, as on Cloud SQL, whose IAM users hold
// cloudsqlsuperuser, a role with CREATEROLE, and Postgres does not inherit
// an attribute. Empty when there is none.
const roleCreator = mine + `
SELECT r.rolname, r.rolname = current_user
FROM pg_roles r JOIN mine ON mine.oid = r.oid
WHERE r.rolcreaterole
ORDER BY r.rolname <> current_user, r.rolname
LIMIT 1`

// canActAs reports whether the connection's user may SET ROLE to $1: a
// member, which from Postgres 16 is a grant WITH SET.
const canActAs = `
SELECT pg_has_role($1::name, CASE WHEN current_setting('server_version_num')::int >= 160000 THEN 'SET' ELSE 'MEMBER' END)`

// schemaOwner is the owner of schema $1.
const schemaOwner = `
SELECT pg_get_userbyid(nspowner)::text FROM pg_namespace WHERE nspname = $1`

// roleMembers are the roles granted $1, but the connection's user and the
// roles it is granted (the role a CREATEROLE grant made a member, on
// Postgres 16).
const roleMembers = mine + `
SELECT DISTINCT r.rolname
FROM pg_auth_members a JOIN pg_roles r ON r.oid = a.member
WHERE a.roleid = $1::text::regrole AND a.member NOT IN (SELECT oid FROM mine)
ORDER BY 1`

// unaccentSchema is the schema the unaccent extension lives in.
const unaccentSchema = `
SELECT n.nspname::text FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'unaccent'`

// GrantEngine readies the engine's schema for the deployables that open
// it, each as a database user of its own (D58), in one transaction: it
// creates the role when it is missing, as a role the connection's user is
// granted that may create roles when the user may not; makes the user a
// member that may act as it; creates the schema owned by the role, or
// gives an existing one to the role; grants the role to each member and
// takes it back from every other role holding it but the user's own. The
// engine acts as its schema's owner, so whatever a member creates there is
// the role's.
//
// Then, in a transaction of its own, it creates the unaccent extension the
// engine's Search needs in migrate.EngineExtensionSchema when the database
// has none, as the engine would where its user may, and gives the role
// USAGE on the schema it lives in. An extension it cannot create is
// reported, not an error: an engine composes Search or does not, and one
// that does says, when it publishes, that an administrator must create
// it.
func (d *Driver) GrantEngine(ctx context.Context, e migrate.EnginePrivileges) (*migrate.EngineGrants, error) {
	out := &migrate.EngineGrants{}
	role := pgx.Identifier{e.Role}.Sanitize()
	schema := pgx.Identifier{e.Schema}.Sanitize()
	err := d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, c migrate.Conn) error {
		exists, err := queryBool(ctx, c, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, e.Role)
		if err != nil {
			return fmt.Errorf("postgres: look for role %s: %w", e.Role, err)
		}
		if !exists {
			if err := createRole(ctx, c, e.Role); err != nil {
				return err
			}
			out.CreatedRole = true
		}
		actAs, err := queryBool(ctx, c, canActAs, e.Role)
		if err != nil {
			return fmt.Errorf("postgres: whether the job may act as %s: %w", e.Role, err)
		}
		if !actAs {
			if err := c.Exec(ctx, "GRANT "+role+" TO CURRENT_USER"); err != nil {
				return fmt.Errorf("postgres: make the job's user a member of %s, which owns the engine's schema: %w", e.Role, err)
			}
		}
		var owner string
		found := false
		err = c.Query(ctx, schemaOwner, []any{e.Schema}, func(scan func(dest ...any) error) error {
			found = true
			return scan(&owner)
		})
		if err != nil {
			return fmt.Errorf("postgres: look for schema %s: %w", e.Schema, err)
		}
		switch {
		case !found:
			if err := c.Exec(ctx, "CREATE SCHEMA "+schema+" AUTHORIZATION "+role); err != nil {
				return fmt.Errorf("postgres: create the engine's schema %s: %w", e.Schema, err)
			}
			out.CreatedSchema = true
		case owner != e.Role:
			if err := c.Exec(ctx, "ALTER SCHEMA "+schema+" OWNER TO "+role); err != nil {
				return fmt.Errorf("postgres: give the engine's schema %s, which %s owns, to %s: %w", e.Schema, owner, e.Role, err)
			}
			out.OwnedSchema = true
		}
		var members []string
		err = c.Query(ctx, roleMembers, []any{e.Role}, func(scan func(dest ...any) error) error {
			var member string
			if err := scan(&member); err != nil {
				return err
			}
			members = append(members, member)
			return nil
		})
		if err != nil {
			return fmt.Errorf("postgres: list the members of %s: %w", e.Role, err)
		}
		for _, member := range members {
			if slices.Contains(e.Members, member) {
				continue
			}
			if err := c.Exec(ctx, "REVOKE "+role+" FROM "+pgx.Identifier{member}.Sanitize()); err != nil {
				return fmt.Errorf("postgres: take %s back from %s: %w", e.Role, member, err)
			}
			out.Revoked = append(out.Revoked, member)
		}
		for _, member := range e.Members {
			if slices.Contains(members, member) {
				continue
			}
			if err := c.Exec(ctx, "GRANT "+role+" TO "+pgx.Identifier{member}.Sanitize()); err != nil {
				return fmt.Errorf("postgres: grant %s to %s: %w", e.Role, member, err)
			}
			out.Granted = append(out.Granted, member)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var extension string
	err = d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, c migrate.Conn) error {
		var err error
		if extension, err = queryString(ctx, c, unaccentSchema); err != nil {
			return err
		}
		if extension == "" {
			extension = migrate.EngineExtensionSchema
			for _, statement := range []string{
				"CREATE SCHEMA IF NOT EXISTS " + pgx.Identifier{extension}.Sanitize(),
				"CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA " + pgx.Identifier{extension}.Sanitize(),
			} {
				if err := c.Exec(ctx, statement); err != nil {
					return refusedExtension{err}
				}
			}
		}
		usable, err := queryBool(ctx, c, `SELECT has_schema_privilege($1::name, $2, 'USAGE')`, e.Role, extension)
		if err != nil {
			return err
		}
		if !usable {
			if err := c.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{extension}.Sanitize()+" TO "+role); err != nil {
				return refusedExtension{err}
			}
		}
		return nil
	})
	var refused refusedExtension
	switch {
	case errors.As(err, &refused):
		out.ExtensionError = refused.err.Error()
	case err != nil:
		return nil, fmt.Errorf("postgres: the unaccent extension: %w", err)
	default:
		out.Extension = extension
	}
	return out, nil
}

// refusedExtension is the database's refusal of the unaccent extension or
// of USAGE on its schema, which rolls its transaction back and is
// reported.
type refusedExtension struct{ err error }

func (r refusedExtension) Error() string { return r.err.Error() }

// createRole creates role, which cannot log in, as the role roleCreator
// names.
func createRole(ctx context.Context, c migrate.Conn, role string) error {
	var creator string
	var self bool
	err := c.Query(ctx, roleCreator, nil, func(scan func(dest ...any) error) error {
		return scan(&creator, &self)
	})
	switch {
	case err != nil:
		return fmt.Errorf("postgres: find a role that may create %s: %w", role, err)
	case creator == "":
		return fmt.Errorf("postgres: create role %s, which owns the engine's schema: the job's user may not create roles, and is granted no role that may", role)
	}
	create := "CREATE ROLE " + pgx.Identifier{role}.Sanitize() + " NOLOGIN"
	if self {
		if err := c.Exec(ctx, create); err != nil {
			return fmt.Errorf("postgres: create role %s, which owns the engine's schema: %w", role, err)
		}
		return nil
	}
	for _, statement := range []string{"SET LOCAL ROLE " + pgx.Identifier{creator}.Sanitize(), create, "RESET ROLE"} {
		if err := c.Exec(ctx, statement); err != nil {
			return fmt.Errorf("postgres: create role %s, which owns the engine's schema, as %s: %w", role, creator, err)
		}
	}
	return nil
}

// queryBool runs a statement that returns one boolean.
func queryBool(ctx context.Context, c migrate.Conn, sql string, args ...any) (bool, error) {
	var out bool
	err := c.Query(ctx, sql, args, func(scan func(dest ...any) error) error { return scan(&out) })
	return out, err
}

// queryString runs a statement that returns at most one text, empty for
// none.
func queryString(ctx context.Context, c migrate.Conn, sql string, args ...any) (string, error) {
	var out string
	err := c.Query(ctx, sql, args, func(scan func(dest ...any) error) error { return scan(&out) })
	return out, err
}
