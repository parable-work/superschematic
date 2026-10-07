package tsreader

import (
	"strings"
	"testing"
)

// number is the value the edit tests record: a GCP project's number.
const number = "123456789012"

// projectNumber names projectNumber in class's gcp values, beside project.
func projectNumber(class string) EnvironmentValue {
	return EnvironmentValue{Class: class, Target: "gcp", Key: "projectNumber", Value: number, Beside: "project"}
}

// TestSetEnvironmentValue covers each layout the edit writes into, with
// the whole file it should leave, byte for byte.
func TestSetEnvironmentValue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src, want string
		previous  string
		found     bool
	}{
		{
			name: "one-line object",
			src: `@environment({ target: "gcp", gcp: { project: "acme-staging", region: "us-east1" } })
export abstract class Staging {}
`,
			want: `@environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber: "123456789012", region: "us-east1" } })
export abstract class Staging {}
`,
		},
		{
			name: "one-line object, beside last",
			src:  `@environment({ target: "gcp", gcp: { region: "us-east1", project: "acme-staging" } }) export abstract class Staging {}`,
			want: `@environment({ target: "gcp", gcp: { region: "us-east1", project: "acme-staging", projectNumber: "123456789012" } }) export abstract class Staging {}`,
		},
		{
			name: "one-line object, trailing comma",
			src:  `@environment({ target: "gcp", gcp: { region: "us-east1", project: "acme-staging", } }) export abstract class Staging {}`,
			want: `@environment({ target: "gcp", gcp: { region: "us-east1", project: "acme-staging", projectNumber: "123456789012", } }) export abstract class Staging {}`,
		},
		{
			name: "one-line object, no spaces, single quotes",
			src:  `@environment({target:'gcp',gcp:{project:'acme-staging',region:'us-east1'}}) export abstract class Staging {}`,
			want: `@environment({target:'gcp',gcp:{project:'acme-staging',projectNumber: '123456789012',region:'us-east1'}}) export abstract class Staging {}`,
		},
		{
			name: "multi-line object",
			src: `@environment({
  target: "gcp",
  gcp: {
    project: "acme-staging",
    region: "us-east1"
  }
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: {
    project: "acme-staging",
    projectNumber: "123456789012",
    region: "us-east1"
  }
})
export abstract class Staging {}
`,
		},
		{
			name: "multi-line object, beside last",
			src: `@environment({
  target: "gcp",
  gcp: {
    region: "us-east1",
    project: "acme-staging"
  }
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: {
    region: "us-east1",
    project: "acme-staging",
    projectNumber: "123456789012"
  }
})
export abstract class Staging {}
`,
		},
		{
			name: "multi-line object, trailing comma",
			src: `@environment({
	target: "gcp",
	gcp: {
		region: "us-east1",
		project: "acme-staging",
	},
})
export abstract class Staging {}
`,
			want: `@environment({
	target: "gcp",
	gcp: {
		region: "us-east1",
		project: "acme-staging",
		projectNumber: "123456789012",
	},
})
export abstract class Staging {}
`,
		},
		{
			name: "first property on the brace's line",
			src: `@environment({
  target: "gcp",
  gcp: { project: "acme-staging",
         region: "us-east1" }
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: { project: "acme-staging",
         projectNumber: "123456789012",
         region: "us-east1" }
})
export abstract class Staging {}
`,
		},
		{
			name: "comments around the property",
			src: `@environment({
  target: "gcp",
  gcp: {
    // The project everything lands in.
    project: "acme-staging", // staging's own
    /* the region */ region: "us-east1",
  },
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: {
    // The project everything lands in.
    project: "acme-staging", // staging's own
    projectNumber: "123456789012",
    /* the region */ region: "us-east1",
  },
})
export abstract class Staging {}
`,
		},
		{
			name: "a comment after the last property",
			src: `@environment({
  target: "gcp",
  gcp: {
    region: "us-east1",
    project: "acme-staging" /* staging's own */ // and nothing else
  }
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: {
    region: "us-east1",
    project: "acme-staging", /* staging's own */ // and nothing else
    projectNumber: "123456789012"
  }
})
export abstract class Staging {}
`,
		},
		{
			name: "CRLF line endings",
			src:  "@environment({\r\n  target: \"gcp\",\r\n  gcp: {\r\n    project: \"acme-staging\",\r\n    region: \"us-east1\",\r\n  },\r\n})\r\nexport abstract class Staging {}\r\n",
			want: "@environment({\r\n  target: \"gcp\",\r\n  gcp: {\r\n    project: \"acme-staging\",\r\n    projectNumber: \"123456789012\",\r\n    region: \"us-east1\",\r\n  },\r\n})\r\nexport abstract class Staging {}\r\n",
		},
		{
			name: "existing value, equal",
			src: `@environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber: "123456789012", region: "us-east1" } })
export abstract class Staging {}
`,
			want: `@environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber: "123456789012", region: "us-east1" } })
export abstract class Staging {}
`,
			previous: number, found: true,
		},
		{
			name: "existing value, different, in its own quotes",
			src: `@environment({
  target: "gcp",
  gcp: {
    project: "acme-staging",
    region: "us-east1",
    "projectNumber": /* recorded */ '111111111111', // by bootstrap
  },
})
export abstract class Staging {}
`,
			want: `@environment({
  target: "gcp",
  gcp: {
    project: "acme-staging",
    region: "us-east1",
    "projectNumber": /* recorded */ '123456789012', // by bootstrap
  },
})
export abstract class Staging {}
`,
			previous: "111111111111", found: true,
		},
		{
			name: "existing value, beside absent",
			src:  `@environment({ target: "gcp", gcp: { region: "us-east1", projectNumber: "111111111111" } }) export abstract class Staging {}`,
			want: `@environment({ target: "gcp", gcp: { region: "us-east1", projectNumber: "123456789012" } }) export abstract class Staging {}`,

			previous: "111111111111", found: true,
		},
		{
			name: "a namespaced decorator",
			src:  `@s.environment({ target: "gcp", gcp: { project: "acme-staging", region: "us-east1" } }) export abstract class Staging {}`,
			want: `@s.environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber: "123456789012", region: "us-east1" } }) export abstract class Staging {}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, previous, found, err := SetEnvironmentValue("src/stack.schema.ts", []byte(tc.src), projectNumber("Staging"))
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", out, tc.want)
			}
			if previous != tc.previous || found != tc.found {
				t.Errorf("previous %q, found %v; want %q, %v", previous, found, tc.previous, tc.found)
			}
		})
	}
}

// shopSchema is a stack schema as an engineer writes one: imports, comments
// and several environments, in the layouts the edit meets.
const shopSchema = `import { ShopApi } from "@acme/shop-api";
import { environment, stack } from "@superschematic/stack";

// The shop as it runs.
@stack({ deploy: [ShopApi], expose: [ShopApi] })
export abstract class Shop {}

/** Staging, which previews extend. */
@environment({
  target: "gcp",
  gcp: {
    project: "acme-staging", // shared with QA
    region: "us-east1",
  },
  domain: "staging.acme.dev",
})
export abstract class Staging {}

@environment({ parameters: ["pr"] })
export abstract class Preview extends Staging {}

// Production: reviewed before each deploy.
@environment({
  target: "gcp",
  gcp: { project: "acme-prod", region: "us-east1", production: true },
  domain: "acme.dev",
  settings: [{ of: ShopApi, minInstances: 1 }],
})
export abstract class Production {}
`

// TestSetEnvironmentValueChangesOnlyTheProperty edits each environment of
// one file in turn, and checks every other byte of the file is the
// engineer's: the file is the original with the property inserted at one
// place, or one literal's text replaced.
func TestSetEnvironmentValueChangesOnlyTheProperty(t *testing.T) {
	src := []byte(shopSchema)
	staging, _, found, err := SetEnvironmentValue("src/stack.schema.ts", src, projectNumber("Staging"))
	if err != nil || found {
		t.Fatalf("Staging: found %v, %v", found, err)
	}
	anchor := `    project: "acme-staging", // shared with QA` + "\n"
	if want := strings.Replace(shopSchema, anchor, anchor+`    projectNumber: "123456789012",`+"\n", 1); string(staging) != want {
		t.Errorf("Staging: got\n%s\nwant\n%s", staging, want)
	}

	production, _, found, err := SetEnvironmentValue("src/stack.schema.ts", staging, EnvironmentValue{
		Class: "Production", Target: "gcp", Key: "projectNumber", Value: "210987654321", Beside: "project",
	})
	if err != nil || found {
		t.Fatalf("Production: found %v, %v", found, err)
	}
	anchor = `gcp: { project: "acme-prod",`
	if want := strings.Replace(string(staging), anchor, anchor+` projectNumber: "210987654321",`, 1); string(production) != want {
		t.Errorf("Production: got\n%s\nwant\n%s", production, want)
	}

	// Staging again with another number replaces its literal's digits and
	// nothing else; Production keeps its own.
	again, previous, found, err := SetEnvironmentValue("src/stack.schema.ts", production, projectNumber("Staging"))
	if err != nil || !found || previous != number || string(again) != string(production) {
		t.Fatalf("Staging again: previous %q, found %v, changed %v, %v", previous, found, string(again) != string(production), err)
	}
	renumbered, previous, found, err := SetEnvironmentValue("src/stack.schema.ts", production, EnvironmentValue{
		Class: "Staging", Target: "gcp", Key: "projectNumber", Value: "999999999999", Beside: "project",
	})
	if err != nil || !found || previous != number {
		t.Fatalf("Staging renumbered: previous %q, found %v, %v", previous, found, err)
	}
	if want := strings.Replace(string(production), `"`+number+`"`, `"999999999999"`, 1); string(renumbered) != want {
		t.Errorf("Staging renumbered: got\n%s\nwant\n%s", renumbered, want)
	}
	if !strings.Contains(string(renumbered), `projectNumber: "210987654321"`) {
		t.Error("renumbering Staging changed Production's number")
	}
}

// TestSetEnvironmentValueRefuses covers each file the edit leaves alone,
// with a reason that says what to set by hand.
func TestSetEnvironmentValueRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, src, class, want string
	}{
		{
			name:  "no such class",
			src:   `@environment({ target: "gcp", gcp: { project: "acme-staging" } }) export abstract class Staging {}`,
			class: "Production",
			want:  "src/stack.schema.ts declares no class Production",
		},
		{
			name:  "no @environment decorator",
			src:   `@stack({ deploy: [] }) export abstract class Staging {}`,
			class: "Staging",
			want:  "src/stack.schema.ts:1:1: class Staging has no @environment({...}) decorator",
		},
		{
			name:  "two @environment decorators",
			src:   "@environment({ target: \"gcp\" })\n@environment({ target: \"gcp\" })\nexport abstract class Staging {}",
			class: "Staging",
			want:  "src/stack.schema.ts:2:2: class Staging has more than one @environment decorator",
		},
		{
			name:  "no object literal",
			src:   `@environment(stagingEnvironment) export abstract class Staging {}`,
			class: "Staging",
			want:  "the @environment decorator of class Staging takes no object literal",
		},
		{
			name:  "no target values",
			src:   `@environment({ target: "local" }) export abstract class Staging {}`,
			class: "Staging",
			want:  "the @environment decorator of class Staging sets no gcp values",
		},
		{
			name:  "target values not an object literal",
			src:   `@environment({ target: "gcp", gcp: stagingValues }) export abstract class Staging {}`,
			class: "Staging",
			want:  "the gcp values of class Staging are not an object literal",
		},
		{
			name:  "a spread among the values",
			src:   `@environment({ target: "gcp", gcp: { ...shared, region: "us-east1" } }) export abstract class Staging {}`,
			class: "Staging",
			want:  "holds ...shared, whose key the edit cannot read",
		},
		{
			name:  "no beside key",
			src:   `@environment({ target: "gcp", gcp: { region: "us-east1" } }) export abstract class Staging {}`,
			class: "Staging",
			want:  "the gcp values of class Staging do not set project, which projectNumber goes beside",
		},
		{
			name:  "beside set twice",
			src:   `@environment({ target: "gcp", gcp: { project: "a-project", project: "b-project" } }) export abstract class Staging {}`,
			class: "Staging",
			want:  "sets project twice",
		},
		{
			name:  "a constant's value",
			src:   "const NUMBER = \"1\";\n@environment({ target: \"gcp\", gcp: { project: \"acme-staging\", projectNumber: NUMBER } }) export abstract class Staging {}",
			class: "Staging",
			want:  "src/stack.schema.ts:2:63: projectNumber in the gcp values of class Staging is not a string literal",
		},
		{
			name:  "a template literal",
			src:   "@environment({ target: \"gcp\", gcp: { project: \"acme-staging\", projectNumber: `1` } }) export abstract class Staging {}",
			class: "Staging",
			want:  "projectNumber in the gcp values of class Staging is not a string literal",
		},
		{
			name:  "a shorthand property",
			src:   `@environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber } }) export abstract class Staging {}`,
			class: "Staging",
			want:  "projectNumber in an object of the @environment decorator of class Staging is not a plain property",
		},
		{
			name:  "a file that does not parse",
			src:   `@environment({ target: "gcp", gcp: { project: "acme-staging" } export abstract class Staging {}`,
			class: "Staging",
			want:  "does not parse",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _, err := SetEnvironmentValue("src/stack.schema.ts", []byte(tc.src), projectNumber(tc.class))
			if err == nil {
				t.Fatalf("edited it:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			byHand := `set projectNumber: "123456789012" in the gcp values of ` + tc.class + " by hand"
			if !strings.HasSuffix(err.Error(), byHand) {
				t.Errorf("err = %v, want it to end %q", err, byHand)
			}
		})
	}
}

// TestQuoteJS writes values a JavaScript string literal holds as written.
func TestQuoteJS(t *testing.T) {
	for in, want := range map[string]string{
		"123":       `"123"`,
		`a"b\c`:     `"a\"b\\c"`,
		"tab\tnl\n": `"tab\tnl\n"`,
		"\x01":      `"\u0001"`,
	} {
		if got := quoteJS(in, '"'); got != want {
			t.Errorf("quoteJS(%q) = %s, want %s", in, got, want)
		}
	}
	if got := quoteJS(`it's`, '\''); got != `'it\'s'` {
		t.Errorf("single-quoted: %s", got)
	}
	for in, want := range map[string]string{"projectNumber": "projectNumber", "$a_1": "$a_1", "1a": `"1a"`, "a-b": `"a-b"`} {
		if got := propertyKey(in); got != want {
			t.Errorf("propertyKey(%q) = %s, want %s", in, got, want)
		}
	}
}
