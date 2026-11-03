package tsreader

import (
	"strings"
	"testing"
)

// TestEncryptedAndNamedFieldsLoad: @superschematic/db's Encrypted<string>
// loads a stored field's EncryptedAtRest, which implies Secret, whether it
// may be null or not; @table names a table and @column a column, a
// relation's foreign key among them, while an @column of alias.field stays
// a projection column's source (D85).
func TestEncryptedAndNamedFieldsLoad(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Encrypted, Relation, column, key, projection, table } from "@superschematic/db";
@table("bankLink")
export abstract class PlaidItem {
  @key
  id: AutoGenerate<Identity.UUID>;
  @column("accessToken")
  accessToken: Encrypted<string>;
  webhookSecret: Nullable<Encrypted<Validate<string, { maxLength: 80 }>>>;
  @column("ownerId")
  owner: Relation<Owner>;
}
export abstract class Owner {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}
@projection<Owner>({ pool: "report", name: "owners", migration: "20260101000000" })
export abstract class OwnerRow {
  @column("base.name")
  label: string;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	item := schema.Types["PlaidItem"]
	if item.SQLName != "bankLink" {
		t.Errorf("PlaidItem's table = %q", item.SQLName)
	}
	fields := map[string]struct {
		encrypted, secret, required bool
		column                      string
	}{}
	for _, fd := range item.Fields {
		fields[fd.Name] = struct {
			encrypted, secret, required bool
			column                      string
		}{fd.EncryptedAtRest, fd.Secret, fd.Required, fd.SQLName}
	}
	if f := fields["accessToken"]; !f.encrypted || !f.secret || !f.required || f.column != "accessToken" {
		t.Errorf("accessToken = %+v", f)
	}
	if f := fields["webhookSecret"]; !f.encrypted || !f.secret || f.required {
		t.Errorf("webhookSecret = %+v", f)
	}
	if f := fields["owner"]; f.column != "ownerId" || f.encrypted {
		t.Errorf("owner = %+v", f)
	}
	label := schema.Types["OwnerRow"].Fields[0]
	if label.ProjectedFrom != "base.name" || label.SQLName != "" {
		t.Errorf("a projection column's @column = %q, %q", label.ProjectedFrom, label.SQLName)
	}
}

// TestEncryptedAndNamedFieldsRefusals: Encrypted<T> marks a stored field,
// never an argument or a result, and @table and @column take a SQL
// identifier, once (D85).
func TestEncryptedAndNamedFieldsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, kind, source, want string
	}{
		{"an argument", "API", `import { HttpMethod, rest } from "@superschematic/api";
import { Encrypted } from "@superschematic/db";
export abstract class Receipt { ok: boolean; }
export class Tokens {
  @rest(HttpMethod.POST, "tokens")
  store(token: Encrypted<string>): Receipt { throw new Error("schema declaration only"); }
}
`, "Encrypted<T> marks a stored field of a DB table; an argument carries the value itself"},
		{"a result", "API", `import { HttpMethod, rest } from "@superschematic/api";
import { Encrypted } from "@superschematic/db";
export class Tokens {
  @rest(HttpMethod.GET, "tokens")
  read(): Encrypted<string> { throw new Error("schema declaration only"); }
}
`, "Encrypted<T> marks a stored field of a DB table; a request or a response carries the value itself"},
		{"a name with a space", "DB", `import { key, table } from "@superschematic/db";
@table("line item")
export abstract class LineItem { @key id: string; }
`, `@table("line item"): a name is letters, digits and underscores, not starting with a digit`},
		{"a column named twice", "DB", `import { column, key } from "@superschematic/db";
export abstract class LineItem {
  @key id: string;
  @column("memoText") @column("memo") memo: string;
}
`, "a field takes one @column"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestAnEncryptedVectorIsNoString: Encrypted<T> takes a string (D85), so
// TypeScript refuses an Encrypted<T> Embedding.Vector, a number[], at its
// own check (D84).
func TestAnEncryptedVectorIsNoString(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Embedding, Identity } from "superscalar";
import { Validate } from "@superschematic/schema";
import { AutoGenerate, Encrypted, key } from "@superschematic/db";
export abstract class Sealed {
  @key
  id: AutoGenerate<Identity.UUID>;
  embedding: Encrypted<Validate<Embedding.Vector, { dimensions: 3 }>>;
}
`})
	_, _, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "does not satisfy the constraint 'string'") {
		t.Fatalf("err = %v, want TypeScript to refuse an Encrypted<T> vector", err)
	}
}
