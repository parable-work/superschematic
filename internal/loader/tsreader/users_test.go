package tsreader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestUserTraitsAreRecorded: the User and UserRole traits of
// fixture-users-db land on TypeDef.User and TypeDef.UserRole. They are not
// TraitRefs, they add no fields, and RawHeritage keeps only the rest of the
// heritage clause.
func TestUserTraitsAreRecorded(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-users-db"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	account, role := schema.Types["Account"], schema.Types["Role"]
	if account == nil || role == nil {
		t.Fatalf("fixture types missing: %v", schema.Types)
	}
	if want := (&ir.UserTrait{Login: "email", Name: "displayName"}); !reflect.DeepEqual(account.User, want) {
		t.Errorf("Account.User = %+v, want %+v", account.User, want)
	}
	if role.UserRole == nil || role.User != nil || account.UserRole != nil {
		t.Errorf("UserRole on Account %v and Role %v, User on Role %v", account.UserRole, role.UserRole, role.User)
	}
	for _, td := range []*ir.TypeDef{account, role} {
		if len(td.Implements) > 0 {
			t.Errorf("%s.Implements = %+v, want none", td.Name, td.Implements)
		}
		if want := (&ir.RawHeritage{Extends: "Auditable"}); !reflect.DeepEqual(td.RawHeritage, want) {
			t.Errorf("%s.RawHeritage = %+v, want %+v", td.Name, td.RawHeritage, want)
		}
	}
	var names []string
	for _, fd := range account.Fields {
		names = append(names, fd.Name)
	}
	if got := strings.Join(names, ","); got != "createdAt,updatedAt,id,email,displayName" {
		t.Errorf("Account fields = %s", got)
	}
	for _, name := range []string{"Auditable", "Note"} {
		if td := schema.Types[name]; td.User != nil || td.UserRole != nil {
			t.Errorf("%s carries a user model trait", name)
		}
	}
}

// TestUserTraitBesideOtherHeritage: the trait is known by its import, so a
// renamed import reads the same, and a table named User can take it. Other
// traits of the same clause stay TraitRefs and stay in RawHeritage; a
// config without name leaves Name empty.
func TestUserTraitBesideOtherHeritage(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { trait } from "@superschematic/schema";
import { User as UserTrait, key, unique } from "@superschematic/db";
@trait()
export abstract class Reviewed {}
export abstract class User implements Reviewed, UserTrait<{ login: "handle" }> {
  @key
  id: Identity.UUID;
  @unique
  handle: Identity.Slug;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	user := schema.Types["User"]
	if want := (&ir.UserTrait{Login: "handle"}); !reflect.DeepEqual(user.User, want) {
		t.Errorf("User.User = %+v, want %+v", user.User, want)
	}
	if want := []ir.TraitRef{{Name: "Reviewed"}}; !reflect.DeepEqual(user.Implements, want) {
		t.Errorf("User.Implements = %+v, want %+v", user.Implements, want)
	}
	if want := (&ir.RawHeritage{Implements: []string{"Reviewed"}}); !reflect.DeepEqual(user.RawHeritage, want) {
		t.Errorf("User.RawHeritage = %+v, want %+v", user.RawHeritage, want)
	}
}

// TestUserTraitRefusals pins each refusal of the traits' form and place,
// at the class or its heritage entry, naming the class and the trait. A
// config the UserConfig constraint already refuses reaches the walk only
// past a @ts-expect-error; without one the compiler refuses it.
func TestUserTraitRefusals(t *testing.T) {
	const table = `{
  @key
  id: Identity.UUID;
  @unique
  email: Contact.Email;
}
`
	const imports = `import { Contact, Identity } from "superscalar";
import { User, UserRole, key, unique } from "@superschematic/db";
`
	cases := []struct {
		name   string
		kind   string
		source string
		want   []string
	}{
		{
			name: "a config that is not a type literal",
			kind: "DB",
			source: imports + `type AccountConfig = { login: "email" };
export abstract class Account implements User<AccountConfig> ` + table,
			want: []string{`a.schema.ts:4:47: Account: the User trait's config must be a type literal of string literals, as in User<{ login: "email" }>`},
		},
		{
			name:   "a config member that is not a string literal",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ login: string }> ` + table,
			want:   []string{`a.schema.ts:3:49: Account: the User trait's config must be a type literal of string literals, as in User<{ login: "email" }>`},
		},
		{
			name:   "an unknown key",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ login: "email"; display: "email" }> ` + table,
			want:   []string{`a.schema.ts:3:65: Account: the User trait's config has unknown key "display"; it takes login and name`},
		},
		{
			name:   "an optional member",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ login: "email"; name?: "email" }> ` + table,
			want:   []string{`a.schema.ts:3:65: Account: the User trait's name must not be optional`},
		},
		{
			name: "a missing login",
			kind: "DB",
			source: imports + `// @ts-expect-error login is missing
export abstract class Account implements User<{ name: "email" }> ` + table,
			want: []string{`a.schema.ts:4:47: Account: the User trait's config needs login, the field a user signs in with`},
		},
		{
			name:   "a missing login fails the compiler first",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ name: "email" }> ` + table,
			want:   []string{`a.schema.ts:3:47:`, `Property 'login' is missing`},
		},
		{
			name: "type arguments on UserRole",
			kind: "DB",
			source: imports + `// @ts-expect-error UserRole is not generic
export abstract class Role implements UserRole<{ login: "email" }> ` + table,
			want: []string{`a.schema.ts:4:39: Role: the UserRole trait takes no type arguments`},
		},
		{
			name:   "both traits on one table",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ login: "email" }>, UserRole ` + table,
			want:   []string{`a.schema.ts:3:1: Account: a table takes the User trait or the UserRole trait, not both`},
		},
		{
			name:   "a trait named twice",
			kind:   "DB",
			source: imports + `export abstract class Account implements User<{ login: "email" }>, User<{ login: "email" }> ` + table,
			want:   []string{`a.schema.ts:3:68: Account: the User trait appears more than once`},
		},
		{
			name: "a base class other tables extend",
			kind: "DB",
			source: imports + `export abstract class Person implements User<{ login: "email" }> ` + table + `export abstract class Staff extends Person {}
export abstract class Customer extends Person {}
export abstract class Group implements UserRole ` + table + `export abstract class Team extends Group {}
`,
			want: []string{
				`a.schema.ts:3:1: Person: the User trait is only allowed on a DB table of a DB schema (a base class gets no table, and Customer, Staff extend Person)`,
				`a.schema.ts:11:1: Group: the UserRole trait is only allowed on a DB table of a DB schema (a base class gets no table, and Team extends Group)`,
			},
		},
		{
			name: "a trait class",
			kind: "DB",
			source: `import { Contact, Identity } from "superscalar";
import { trait } from "@superschematic/schema";
import { User, key, unique } from "@superschematic/db";
@trait()
export abstract class Person implements User<{ login: "email" }> ` + table,
			want: []string{`a.schema.ts:4:1: Person: the User trait is only allowed on a DB table of a DB schema (this type has role Trait)`},
		},
		{
			name: "a @jsonField type",
			kind: "DB",
			source: `import { Contact, Identity } from "superscalar";
import { jsonField } from "@superschematic/schema";
import { User, key, unique } from "@superschematic/db";
@jsonField
export abstract class Person implements User<{ login: "email" }> ` + table,
			want: []string{`a.schema.ts:4:1: Person: the User trait is only allowed on a DB table of a DB schema (a @jsonField type is stored as JSON and gets no table)`},
		},
		{
			name: "a General schema",
			kind: "General",
			source: `import { Contact } from "superscalar";
import { User, UserRole } from "@superschematic/db";
export abstract class Person implements User<{ login: "email" }> {
  email: Contact.Email;
}
export abstract class Group implements UserRole {
  name: string;
}
`,
			want: []string{
				`a.schema.ts:3:1: Person: the User trait is only allowed on a DB table of a DB schema (this service is kind General)`,
				`a.schema.ts:6:1: Group: the UserRole trait is only allowed on a DB table of a DB schema (this service is kind General)`,
			},
		},
		{
			name: "an API schema",
			kind: "API",
			source: `import { Contact } from "superscalar";
import { HttpMethod, rest } from "@superschematic/api";
import { User } from "@superschematic/db";
export abstract class Person implements User<{ login: "email" }> {
  email: Contact.Email;
}
export class PersonQueries implements User<{ login: "email" }> {
  @rest(HttpMethod.GET, "people")
  people(): Person[] {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{
				`a.schema.ts:4:1: Person: the User trait is only allowed on a DB table of a DB schema (this service is kind API)`,
				`a.schema.ts:7:39: PersonQueries: the User trait is only allowed on a DB table of a DB schema (an operation set is not one)`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected schema errors")
			}
			msg := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("missing diagnostic %q in:\n%s", want, msg)
				}
			}
		})
	}
}
