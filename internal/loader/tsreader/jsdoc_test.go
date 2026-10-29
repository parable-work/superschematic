package tsreader

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// loadPatchedDisplay copies fixture-display into a temporary service next
// to it, replaces old with replacement in its ticket schema, then each
// further pair's first with its second, and loads it.
func loadPatchedDisplay(t *testing.T, old, replacement string, more ...string) (*ir.Schema, error) {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Join("testdata", "services"), "fixture-display-patched-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "services", "fixture-display"))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "src", "ticket.schema.ts")
	source, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	pairs := append([]string{old, replacement}, more...)
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(text, pairs[i]) {
			t.Fatalf("fixture has no %q to replace", pairs[i])
		}
		text = strings.Replace(text, pairs[i], pairs[i+1], 1)
	}
	if err := os.WriteFile(filename, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	schema, _, err := LoadService(dir)
	return schema, err
}

// TestAnEnumMemberTakesItsTitleFromJSDoc (D48, amended): a line of a
// member's JSDoc comment that begins with "@title <text>" is the member's
// title, and leaves its comment; the rest of every comment on the member
// stays its comment, read as leadingComment reads it.
func TestAnEnumMemberTakesItsTitleFromJSDoc(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-display"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ir.EnumValueDef{
		{Name: "Low", SerializedAs: "low"},
		{Name: "High", Title: "Needs attention", Comment: "Within the week.", SerializedAs: "high"},
		{Name: "Urgent", Title: "Drop everything", Comment: "Drop everything else.", SerializedAs: "urgent"},
	}
	if got := schema.Enums["Priority"].Values; !reflect.DeepEqual(got, want) {
		t.Fatalf("Priority values = %+v, want %+v", got, want)
	}

	urgent := func(title, comment string) ir.EnumValueDef {
		return ir.EnumValueDef{Name: "Urgent", Title: title, Comment: comment, SerializedAs: "urgent"}
	}
	for _, tc := range []struct {
		name, member string
		want         ir.EnumValueDef
	}{
		{"a tag after the comment's lines, then blank lines and another tag", `/**
   * Drop everything else.
   * @title Drop   everything
   *
   * @deprecated Page whoever is on call.
   */
  Urgent = "urgent",`, urgent("Drop   everything", "Drop everything else.\n@deprecated Page whoever is on call.")},
		{"a tag on the block's last line", `/**
   * Drop everything else.
   * @title Drop everything */
  Urgent = "urgent",`, urgent("Drop everything", "Drop everything else.")},
		{"a tag on the block's first line", `/** @title Drop everything
   */
  Urgent = "urgent",`, urgent("Drop everything", "")},
		{"a line without its star", `/**
   Drop everything else.
   @title Drop everything
   */
  Urgent = "urgent",`, urgent("Drop everything", "")},
		{"a block with CRLF line endings", "/**\r\n   * Drop everything else.\r\n   * @title Drop everything\r\n   */\r\n  Urgent = \"urgent\",", urgent("Drop everything", "Drop everything else.")},
		{"a word that begins with @title is comment text", `/**
   * Says @titles in passing.
   * @title-case is no tag either.
   * Mail a@title.example.
   */
  Urgent = "urgent",`, urgent("", "Says @titles in passing.\n@title-case is no tag either.\nMail a@title.example.")},
		{"a block comment beside the tag's", `/* Drop everything else. */
  /** @title Drop everything */
  Urgent = "urgent",`, urgent("Drop everything", "Drop everything else.")},
		{"a block comment on the tag's line", `/* Drop everything else. */ /** @title Drop everything */
  Urgent = "urgent",`, urgent("Drop everything", "Drop everything else.")},
		{"a block comment that ends on the tag's line", `/* Drop everything else.
   */ /** @title Drop everything */
  Urgent = "urgent",`, urgent("Drop everything", "Drop everything else.")},
		{"a line comment after the tag's block", `/** @title Drop everything */ // Drop everything else.
  Urgent = "urgent",`, urgent("Drop everything", "Drop everything else.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loadPatchedDisplay(t, `/**
   * Drop everything else.
   * @title Drop everything
   */
  Urgent = "urgent",`, tc.member)
			if err != nil {
				t.Fatal(err)
			}
			if got := schema.Enums["Priority"].Values[2]; got != tc.want {
				t.Fatalf("Urgent = %+v, want %+v", got, tc.want)
			}
		})
	}

	// Anywhere but an enum member, a @title line is comment text.
	schema, err = loadPatchedDisplay(t, "// How soon a ticket needs an answer.\n", "// How soon a ticket needs an answer.\n/** @title Priority */\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Enums["Priority"].Comment; got != "How soon a ticket needs an answer.\n@title Priority" {
		t.Fatalf("Priority comment = %q", got)
	}
}

// TestAMembersCommentIsReadAsLeadingCommentReadsIt: a member's comment,
// which memberTitle reads comment by comment, is the one leadingComment
// reads line by line from the same trivia, when each comment starts its
// own line, as every other node's is: the comments attached to it,
// undecorated (D100).
func TestAMembersCommentIsReadAsLeadingCommentReadsIt(t *testing.T) {
	for _, trivia := range []string{
		"",
		"\n  ",
		"\n  // One.\n  //\n  // Two.\n  ",
		"\n  /** One. */\n  ",
		"\n  /**\n   * One.\n   *\n   * Two. */\n  ",
		"\n  /**\n   One without its star.\n   * Two.\n   **/\n  ",
		"\n  /* One.\n   * Two.\n   */\n  // Three.\n  /** Four. */\n  ",
		"\n  /**/\n  /***/\n  /** */\n  ",
		"\r\n  // One.\r\n  /**\r\n   * Two.\r\n   */\r\n  ",
		"\n  // ---- Plaid ----\n\n  // One.\n  ",
		"\n  // ---- Plaid ----\n  //\n  /** One.\n   * -----\n   */\n  ",
		"\r\n  /* ===== */\r\n  // =====\r\n  // Accounts\r\n  // =====\r\n  // One.\r\n  ",
		"\n  /** One. */\n\n  ",
		"\n  // One.\r\n\r\n  // Two.\n  ",
	} {
		// leadingComment reads "/**/" as the comment "/".
		want := strings.Join(slices.DeleteFunc(strings.Split(triviaComment(trivia), "\n"), func(s string) bool { return s == "/" }), "\n")
		title, got, err := triviaTitle(trivia)
		if err != nil || title != "" || got != want {
			t.Errorf("triviaTitle(%q) = %q, %q, %v; want the comment %q", trivia, title, got, err, want)
		}
	}
}

// TestDisplayIconAndMultilineLoadIntoIR: @display's icon on a type, and
// @display({ multiline }) on a field, a spec of its own (D48, amended).
func TestDisplayIconAndMultilineLoadIntoIR(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-display"))
	if err != nil {
		t.Fatal(err)
	}
	ticket := schema.Types["Ticket"]
	if ticket.Display == nil || ticket.Display.Icon != "ticket" {
		t.Fatalf("Ticket display = %+v", ticket.Display)
	}
	for _, f := range ticket.Fields {
		multiline := f.Display != nil && f.Display.Multiline != nil && *f.Display.Multiline
		if multiline != (f.Name == "body") {
			t.Errorf("field %s display = %+v", f.Name, f.Display)
		}
	}
	schema, err = loadPatchedDisplay(t, "@display({ multiline: true })", "@display({ multiline: false })")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range schema.Types["Ticket"].Fields {
		if f.Name == "body" && (f.Display == nil || f.Display.Multiline == nil || *f.Display.Multiline) {
			t.Fatalf("body display = %+v, want multiline false", f.Display)
		}
	}
}

// TestDisplayCurrencyLoadsIntoIR: @display({ currency }) on a field (D48,
// amended) reads as a literal code or a sibling field, and a code that is
// not three capital letters fails the read.
func TestDisplayCurrencyLoadsIntoIR(t *testing.T) {
	const fields = `@display({ multiline: true })
  body?: string;

  @display({ currency: "GBP" })
  estimate?: Finance.Money;

  @display({ currency: { field: "budgetCurrency" } })
  budget: Generic.Int64[];

  budgetCurrency?: Finance.Currency;`
	load := func(t *testing.T, replacement string) (*ir.Schema, error) {
		t.Helper()
		const imports = `import { Validate, behavior, display, docs, icon } from "@superschematic/schema";`
		return loadPatchedDisplay(t, "@display({ multiline: true })\n  body?: string;", replacement,
			imports, `import { Finance, Generic } from "superscalar";`+"\n"+imports)
	}
	schema, err := load(t, fields)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ir.DisplayCurrency{}
	for _, f := range schema.Types["Ticket"].Fields {
		if f.Display != nil && f.Display.Currency != nil {
			got[f.Name] = *f.Display.Currency
		}
	}
	want := map[string]ir.DisplayCurrency{"estimate": {Code: "GBP"}, "budget": {Field: "budgetCurrency"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("currencies = %+v, want %+v", got, want)
	}

	// A code is three capital letters, as the argument's JSON Schema
	// says; whether it is an active code, and whether the field holds an
	// amount, is the loader's check (verify.checkFieldDisplays).
	if _, err := load(t, "body?: string;\n\n  @display({ currency: \"gbp\" })\n  fee?: Finance.Money;"); err == nil || !strings.Contains(err.Error(), "@display") {
		t.Fatalf("a lower-case code loaded: %v", err)
	}
}

// TestAMisplacedOrEmptyTitleIsRefused (D48, amended): a @title the reader
// would not read as the member's whole title fails the load, each with its
// message at the member: an empty one, one without a space, a second, one
// that wraps, one in a // or /* */ comment, one that does not begin its
// line, one on a member with two JSDoc comments, and one in a comment a
// blank line parts from the member, which is not the member's (D100).
func TestAMisplacedOrEmptyTitleIsRefused(t *testing.T) {
	const prefix = "enum Priority member High: "
	const (
		misplaced = prefix + "@title is read from a JSDoc comment (/** @title … */), not a // or /* */ comment"
		wrapped   = prefix + "@title must fit on its line"
		midLine   = prefix + "@title must begin a line of the member's JSDoc comment"
		blocks    = prefix + "@title is read from the member's only JSDoc comment, and it has more than one"
		detached  = prefix + "@title is read from the member's own comment, and a blank line parts this one from the member"
	)
	for _, tc := range []struct{ name, comment, want string }{
		{"an empty tag", "/** @title */", prefix + "@title needs the member's title after it"},
		{"an empty tag in a block", "/**\n   * @title\n   */", prefix + "@title needs the member's title after it"},
		{"two tags in a block", "/**\n   * @title Needs attention\n   * @title Look soon\n   */", prefix + "@title is given twice"},
		{"two blocks", "/** @title Needs attention */\n  /** @title Look soon */", prefix + "@title is given twice"},
		{"a // comment", "// @title Needs attention", misplaced},
		{"a /* */ comment", "/* @title Needs attention */", misplaced},
		{"a /* */ block's line", "/*\n   * @title Needs attention\n   */", misplaced},
		{"a /* */ block's line without its star", "/*\n   @title Needs attention\n   */", misplaced},
		{"a // comment after a JSDoc block on its line", "/** Within the week. */ // @title Needs attention", misplaced},
		{"a mention in a // comment", "// Says @title in passing.", misplaced},
		{"a wrapped tag", "/**\n   * @title Needs\n   *   attention\n   */", wrapped},
		{"a wrapped tag on the block's first line", "/** @title Needs\n     attention */", wrapped},
		{"comment text after the tag", "/**\n   * @title Needs attention\n   *\n   * Look within the week.\n   */", wrapped},
		{"a tag without a space", "/** @title:Needs attention */", prefix + "@title needs a space before the member's title"},
		{"a mention in the JSDoc block", "/** Says @title in passing. */", midLine},
		{"a mention in a line of the JSDoc block", "/**\n   * Within the week, says (@title Needs attention).\n   */", midLine},
		{"a tag in a second JSDoc block", "/** Within the week. */ /** @title Needs attention */", blocks},
		{"a tag ahead of a second JSDoc block", "/** @title Needs attention */\n  /** Within the week. */", blocks},
		{"a tag a blank line parts from the member", "/** @title Needs attention */\n\n  /** Within the week. */", detached},
		{"a tag a blank line parts from the member's comment", "/** @title Needs attention */\n\n  // Within the week.", detached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPatchedDisplay(t, "/** @title Needs attention */", tc.comment)
			if err == nil {
				t.Fatal("expected schema errors")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %s\nwant it to contain %s", msg, tc.want)
			}
			if !regexp.MustCompile(`ticket\.schema\.ts:\d+:\d+: ` + regexp.QuoteMeta(tc.want)).MatchString(msg) {
				t.Fatalf("diagnostic should carry file:line:col, got: %s", msg)
			}
		})
	}

	// Two members' titles must differ, ignoring case: Schema.Validate's
	// rule, which the loader applies to every form.
	_, err := loadPatchedDisplay(t, "@title Drop everything", "@title NEEDS ATTENTION")
	if err == nil || !strings.Contains(err.Error(), `enum Priority: members High and Urgent share the title "NEEDS ATTENTION"`) {
		t.Fatalf("a title two members share = %v", err)
	}
}

// TestADependencysEnumKeepsItsTitles (D48, amended): a dependency's enum
// that a local field uses is recorded for the loader's checks of the
// field's defaults, its members read as a local enum's are, titles
// included.
// The dependent's IR does not copy the enum: a UI labels its members from
// the dependency's own schema.
func TestADependencysEnumKeepsItsTitles(t *testing.T) {
	dir, err := os.MkdirTemp(filepath.Join("testdata", "services"), "fixture-display-dependent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	name := filepath.Base(dir)
	for rel, content := range map[string]string{
		"package.json":       `{ "name": "@schemas/` + name + `", "private": true }`,
		"tsconfig.json":      `{ "extends": "../../tsconfig.base.json", "include": ["src/**/*.ts"] }`,
		"schema.config.json": `{ "name": "` + name + `", "kind": "General", "dependencies": [{ "name": "fixture-display", "kind": "General" }], "outputs": {} }`,
		"src/index.ts":       "export * from \"./triage.schema\";\n",
		"src/triage.schema.ts": `import { Priority } from "@schemas/fixture-display";

export abstract class Triage {
  priority: Priority;
}
`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, in, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	priority := in.ExternalEnums["Priority"]
	if priority == nil || priority.Owner != "@schemas/fixture-display" {
		t.Fatalf("Priority = %+v", priority)
	}
	want := []ir.EnumValueDef{
		{Name: "Low", SerializedAs: "low"},
		{Name: "High", Title: "Needs attention", Comment: "Within the week.", SerializedAs: "high"},
		{Name: "Urgent", Title: "Drop everything", Comment: "Drop everything else.", SerializedAs: "urgent"},
	}
	if !reflect.DeepEqual(priority.Values, want) {
		t.Fatalf("Priority values = %+v, want %+v", priority.Values, want)
	}
}

// TestBannersAndDetachedCommentsAreNoDocs (D100): a node's comment is the
// comments no blank line parts from it, without the rules and banners a
// source file marks its sections with. A rule is three or more of
// -=*#~_+/ alone; a banner a title framed by such runs, each set off by a
// space; a title between two rules is a banner too. They are dropped at
// the comment's head, with the empty lines after them, and a rule at its
// tail; a rule between lines of text is Markdown's and stays, and so does
// text that only holds dashes. // and /** */ read alike.
func TestBannersAndDetachedCommentsAreNoDocs(t *testing.T) {
	for _, tc := range []struct{ name, trivia, want string }{
		{"a banner", "// ---- Plaid ----\n", ""},
		{"a banner run to the margin", "// ---- Plaid -----------------------------------------------------------\n", ""},
		{"a banner of another rule", "// === Accounts ===\n", ""},
		{"a banner of hashes", "// #### Accounts ####\n", ""},
		{"a banner in a block", "/* ----- Accounts ----- */\n", ""},
		{"a rule", "// -----\n", ""},
		{"a rule of equals", "// =====\n", ""},
		{"a rule of stars in a block", "/* ***** */\n", ""},
		{"a block that is a rule", "/*************/\n", ""},
		{"a rule of slashes", "//////////////\n", ""},
		{"a spaced rule", "// = = = = =\n", ""},
		{"a title between rules", "// =====\n// Accounts\n// =====\n", ""},
		{"a title between rules, then the doc", "// =====\n// Accounts\n// =====\n// Every account a user holds.\n", "Every account a user holds."},
		{"a banner, then the doc", "// ---- Plaid ----\n// The state of a linked Plaid item.\n", "The state of a linked Plaid item."},
		{"a banner, an empty line, then the doc", "// ---- the dashboard's filter ------\n//\n// Each is a nullable parameter:\n// null means no filter.\n", "Each is a nullable parameter:\nnull means no filter."},
		{"a JSDoc block whose first line is a banner", "/**\n * ---- Plaid ----\n * The state of a linked Plaid item.\n */\n", "The state of a linked Plaid item."},
		{"a block whose first line is a banner", "/* ---- Plaid ----\n * The state of a linked Plaid item.\n */\n", "The state of a linked Plaid item."},
		{"a rule at the tail", "/**\n * The state of a linked Plaid item.\n *\n * -----------\n */\n", "The state of a linked Plaid item."},
		{"a rule between lines of text", "/**\n * The state.\n *\n * ---\n *\n * Its history.\n */\n", "The state.\n---\nIts history."},
		{"a dash in the text", "// Amount - in cents\n", "Amount - in cents"},
		{"dashes in the text", "// Amount -- in cents --- or a refund\n", "Amount -- in cents --- or a refund"},
		{"a run on one side", "/** ### Examples */\n", "### Examples"},
		{"runs that are emphasis", "// ***Settled*** amounts only\n", "***Settled*** amounts only"},
		{"runs with no space", "// ===Accounts===\n", "===Accounts==="},
		{"a banner parted by a blank line", "// ---- Plaid ----\n\n", ""},
		{"a banner parted from the doc", "// ---- Plaid ----\n\n// The state of a linked Plaid item.\n", "The state of a linked Plaid item."},
		{"a note parted from the doc", "// TODO: fold into Account.\n\n/** The state of a linked Plaid item. */\n", "The state of a linked Plaid item."},
		{"a doc parted from the node", "/** The state of a linked Plaid item. */\n\n", ""},
		{"a doc parted by a blank line of spaces", "// One.\n   \t\n  // Two.\n  ", "Two."},
		{"a doc parted by CRLF", "// One.\r\n\r\n// Two.\r\n", "Two."},
		{"a blank line inside a block parts nothing", "/**\n * One.\n\n * Two.\n */\n", "One.\nTwo."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := triviaComment(tc.trivia); got != tc.want {
				t.Errorf("triviaComment(%q) = %q, want %q", tc.trivia, got, tc.want)
			}
			// A member reads its comment alike.
			if title, got, err := triviaTitle(tc.trivia); err != nil || title != "" || got != tc.want {
				t.Errorf("triviaTitle(%q) = %q, %q, %v; want the comment %q", tc.trivia, title, got, err, tc.want)
			}
		})
	}
}

// TestASectionBannerDocumentsNothing (D100): in a schema file, a banner
// before an enum, a table and a field, parted from it by a blank line or
// not, becomes none of their docs, where it was the enum's generated doc
// and the table's COMMENT ON TABLE; the comment under a banner does.
func TestASectionBannerDocumentsNothing(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { key } from "@superschematic/db";

// ---- Plaid -----------------------------------------------------------------

export enum PlaidItemStatus {
  Active = "active",
  // ---- failures ----
  LoginRequired = "login_required",
}

// ============================================================================
// Plaid items
// ============================================================================
// A bank connection, one per institution a user links.
export abstract class PlaidItem {
  @key id: Identity.UUID;

  // ---- sync -----------------------------------------------------------------

  cursor: string;
  // The status Plaid last reported - see PlaidItemStatus.
  status: PlaidItemStatus;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Enums["PlaidItemStatus"].Comment; got != "" {
		t.Errorf("PlaidItemStatus comment = %q, want none", got)
	}
	if got := schema.Enums["PlaidItemStatus"].Values[1].Comment; got != "" {
		t.Errorf("PlaidItemStatus.LoginRequired comment = %q, want none", got)
	}
	item := schema.Types["PlaidItem"]
	if item.Comment != "A bank connection, one per institution a user links." {
		t.Errorf("PlaidItem comment = %q", item.Comment)
	}
	comments := map[string]string{}
	for _, f := range item.Fields {
		comments[f.Name] = f.Comment
	}
	want := map[string]string{"id": "", "cursor": "", "status": "The status Plaid last reported - see PlaidItemStatus."}
	if !reflect.DeepEqual(comments, want) {
		t.Errorf("PlaidItem field comments = %q, want %q", comments, want)
	}
}
