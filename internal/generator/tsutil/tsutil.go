package tsutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// ToCamelCase is the camelCase the TypeScript SDK names its members and
// the tool documents their parameters with: codegen.JoinCamelCase.
// Example: "send_magic_link" -> "sendMagicLink", "SendMagicLink" -> "sendMagicLink"
func ToCamelCase(name string) string {
	return codegen.JoinCamelCase(name)
}

// ToClassName converts a name to TypeScript class name (PascalCase):
// codegen.JoinPascalCase.
// Example: "auth" -> "Auth", "my-namespace" -> "MyNamespace"
func ToClassName(name string) string {
	return codegen.JoinPascalCase(name)
}

// IRTypeToTSType converts an IR type-reference name to a TypeScript wire
// type. Bare language primitives are spelled exactly like the TypeScript
// primitives and pass through; named scalars travel as strings on the wire.
func IRTypeToTSType(typeName string) string {
	switch typeName {
	case "string", "number", "boolean":
		return typeName
	case "JSON":
		return "Record<string, any>"
	default:
		return "string" // Named scalars serialize as strings.
	}
}

// EscapeString escapes s for a double-quoted TypeScript string literal,
// which is then a JSON string too: a backslash, a double quote, a newline,
// a carriage return and a tab are escaped as \\, \", \n, \r and \t, any
// other control character and the line and paragraph separators (U+2028,
// U+2029) as \uXXXX.
func EscapeString(s string) string {
	return escapeString(s, '"')
}

// EscapeSingleQuoted escapes s for a single-quoted TypeScript string
// literal, as EscapeString does for a double-quoted one.
func EscapeSingleQuoted(s string) string {
	return escapeString(s, '\'')
}

// Quote writes s as a single-quoted TypeScript string literal.
func Quote(s string) string {
	return "'" + EscapeSingleQuoted(s) + "'"
}

// EscapeTemplateLiteral escapes s for a TypeScript template literal, which
// holds its newlines as they are: a backslash, a backquote and the ${ that
// would start a substitution are escaped.
func EscapeTemplateLiteral(s string) string {
	return templateLiteralEscaper.Replace(s)
}

var templateLiteralEscaper = strings.NewReplacer(`\`, `\\`, "`", "\\`", "${", `\${`)

func escapeString(s string, quote rune) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case quote:
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\u2028', '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// FormatTypeScript runs prettier on TypeScript files in the given directory
func FormatTypeScript(outputDir string) error {
	_, err := exec.LookPath("bunx")
	if err != nil {
		return nil
	}

	var tsFiles []string
	err = filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".ts") {
			relPath, err := filepath.Rel(outputDir, path)
			if err != nil {
				return err
			}
			tsFiles = append(tsFiles, relPath)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to find TypeScript files: %w", err)
	}

	if len(tsFiles) == 0 {
		return nil
	}

	args := []string{"prettier@3", "--write", "--config", ".prettierrc.json"}
	args = append(args, tsFiles...)

	cmd := exec.Command("bunx", args...)
	cmd.Dir = outputDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("prettier formatting failed (non-fatal): %w\nOutput: %s", err, string(output))
	}

	return nil
}

// CopyPrettierConfig creates a minimal .prettierrc.json for TypeScript files in output directory
func CopyPrettierConfig(projectRoot, outputDir string) error {
	// Create a minimal prettier config without plugins (like svelte)
	// that might not be available in the bunx context
	prettierConfig := `{
  "jsxBracketSameLine": false,
  "bracketSpacing": true,
  "tabWidth": 2,
  "useTabs": false,
  "singleQuote": true,
  "semi": true,
  "quoteProps": "as-needed",
  "proseWrap": "preserve",
  "arrowParens": "avoid",
  "trailingComma": "es5",
  "printWidth": 100,
  "embeddedLanguageFormatting": "auto"
}
`

	prettierrcDest := filepath.Join(outputDir, ".prettierrc.json")
	if err := os.WriteFile(prettierrcDest, []byte(prettierConfig), 0644); err != nil {
		return fmt.Errorf("failed to write .prettierrc.json: %w", err)
	}

	return nil
}

// CompileTypeScript runs tsc to compile TypeScript files and generate declaration files
func CompileTypeScript(outputDir string) error {
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun not found: %w", err)
	}

	installCmd := exec.Command("bun", "install")
	installCmd.Dir = outputDir
	installOutput, err := installCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bun install failed: %w\nOutput: %s", err, string(installOutput))
	}

	buildCmd := exec.Command("bun", "run", "build")
	buildCmd.Dir = outputDir

	buildOutput, err := buildCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("TypeScript compilation failed: %w\nOutput: %s", err, string(buildOutput))
	}

	return nil
}
