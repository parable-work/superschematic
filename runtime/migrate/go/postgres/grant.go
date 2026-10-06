package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

var _ migrate.Granter = (*Driver)(nil)

// grantable lists the objects the connection's user may give privileges
// on: the tables, partitioned tables, views, materialized views and
// sequences outside the system schemas, owned by the user or a role it
// inherits, which no extension owns, with the schema of each and whether
// the user may give USAGE on that schema. $1 lists the tables to leave
// out.
const grantable = `
SELECT n.nspname, c.relname, c.relkind::text,
       has_schema_privilege(n.oid, 'USAGE WITH GRANT OPTION')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S')
  AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND pg_has_role(c.relowner, 'USAGE')
  AND NOT (c.relname = ANY($1::text[]) AND c.relkind IN ('r', 'p'))
  AND NOT EXISTS (
    SELECT 1 FROM pg_depend d
    WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY n.nspname, c.relname`

// grantees lists the roles the connection's user, or a role it inherits,
// gave privileges on the objects grantable lists or on a schema it owns,
// other than the owners themselves and PUBLIC.
const grantees = `
SELECT r.rolname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL aclexplode(c.relacl) a
JOIN pg_roles r ON r.oid = a.grantee
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S')
  AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND pg_has_role(c.relowner, 'USAGE')
  AND pg_has_role(a.grantor, 'USAGE')
  AND a.grantee <> c.relowner
UNION
SELECT r.rolname
FROM pg_namespace n
CROSS JOIN LATERAL aclexplode(n.nspacl) a
JOIN pg_roles r ON r.oid = a.grantee
WHERE n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND pg_has_role(n.nspowner, 'USAGE')
  AND pg_has_role(a.grantor, 'USAGE')
  AND a.grantee <> n.nspowner
ORDER BY 1`

// GrantReadWrite gives each role USAGE on the schemas that hold the
// database's objects, SELECT, INSERT, UPDATE and DELETE on its tables,
// SELECT on its views and USAGE and SELECT on its sequences, and takes
// every privilege on them back from any other role the connection's user
// gave privileges to, all in one transaction. It leaves out the tables of
// except, extensions' objects and objects of system schemas, and a schema
// the user may not give USAGE on, such as one only PUBLIC may use.
func (d *Driver) GrantReadWrite(ctx context.Context, roles []string, except []string) (*migrate.Grants, error) {
	out := &migrate.Grants{}
	err := d.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, c migrate.Conn) error {
		var tables, views, sequences, schemas []string
		err := c.Query(ctx, grantable, []any{exceptArray(except)}, func(scan func(dest ...any) error) error {
			var schema, name, kind string
			var schemaGrant bool
			if err := scan(&schema, &name, &kind, &schemaGrant); err != nil {
				return err
			}
			qualified := pgx.Identifier{schema, name}.Sanitize()
			switch kind {
			case "r", "p":
				tables = append(tables, qualified)
			case "v", "m":
				views = append(views, qualified)
			case "S":
				sequences = append(sequences, qualified)
			}
			q := pgx.Identifier{schema}.Sanitize()
			if schemaGrant && !slices.Contains(schemas, q) {
				schemas = append(schemas, q)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("postgres: list the objects to grant on: %w", err)
		}
		var others []string
		err = c.Query(ctx, grantees, nil, func(scan func(dest ...any) error) error {
			var role string
			if err := scan(&role); err != nil {
				return err
			}
			if !slices.Contains(roles, role) {
				others = append(others, role)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("postgres: list the roles granted: %w", err)
		}
		for _, role := range others {
			r := pgx.Identifier{role}.Sanitize()
			for _, revoke := range []struct {
				what    string
				objects []string
			}{
				{"ALL ON TABLE", append(slices.Clone(tables), views...)},
				{"ALL ON SEQUENCE", sequences},
				{"USAGE ON SCHEMA", schemas},
			} {
				if len(revoke.objects) == 0 {
					continue
				}
				if err := c.Exec(ctx, fmt.Sprintf("REVOKE %s %s FROM %s", revoke.what, strings.Join(revoke.objects, ", "), r)); err != nil {
					return fmt.Errorf("postgres: revoke the privileges of %s: %w", role, err)
				}
			}
		}
		for _, role := range roles {
			r := pgx.Identifier{role}.Sanitize()
			for _, grant := range []struct {
				what    string
				objects []string
			}{
				{"USAGE ON SCHEMA", schemas},
				{"SELECT, INSERT, UPDATE, DELETE ON TABLE", tables},
				{"SELECT ON TABLE", views},
				{"USAGE, SELECT ON SEQUENCE", sequences},
			} {
				if len(grant.objects) == 0 {
					continue
				}
				if err := c.Exec(ctx, fmt.Sprintf("GRANT %s %s TO %s", grant.what, strings.Join(grant.objects, ", "), r)); err != nil {
					return fmt.Errorf("postgres: grant %s privileges: %w", role, err)
				}
			}
		}
		out.Objects = len(tables) + len(views) + len(sequences)
		out.Schemas = len(schemas)
		out.Revoked = others
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// exceptArray is except as a Postgres text array literal argument.
func exceptArray(except []string) []string {
	if except == nil {
		return []string{}
	}
	return except
}
