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
// to it, replaces old with replacement in its ticket schema, and loads it.
func loadPatchedDisplay(t *testing.T, old, replacement string) (*ir.Schema, error) {
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
	if !strings.Contains(string(source), old) {
		t.Fatalf("fixture has no %q to replace", old)
	}
	if err := os.WriteFile(filename, []byte(strings.Replace(string(source), old, replacement, 1)), 0o600); err != nil {
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
// own line, as every other node's is.
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
	} {
		var lines []string
		for _, raw := range strings.Split(trivia, "\n") {
			if s, ok := commentLine(strings.TrimSpace(raw)); ok {
				lines = append(lines, s)
			}
		}
		// leadingComment reads "/**/" as the comment "/".
		want := strings.Join(slices.DeleteFunc(lines, func(s string) bool { return s == "/" }), "\n")
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

// TestAMisplacedOrEmptyTitleIsRefused (D48, amended): a @title the reader
// would not read as the member's whole title fails the load, each with its
// message at the member: an empty one, one without a space, a second, one
// that wraps, one in a // or /* */ comment, one that does not begin its
// line, and one on a member with two JSDoc comments.
func TestAMisplacedOrEmptyTitleIsRefused(t *testing.T) {
	const prefix = "enum Priority member High: "
	const (
		misplaced = prefix + "@title is read from a JSDoc comment (/** @title … */), not a // or /* */ comment"
		wrapped   = prefix + "@title must fit on its line"
		midLine   = prefix + "@title must begin a line of the member's JSDoc comment"
		blocks    = prefix + "@title is read from the member's only JSDoc comment, and it has more than one"
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
