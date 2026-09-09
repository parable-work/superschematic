package tsutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ToCamelCase converts a string to camelCase
// Handles snake_case, kebab-case, and PascalCase
// Example: "send_magic_link" -> "sendMagicLink", "SendMagicLink" -> "sendMagicLink"
func ToCamelCase(name string) string {
	// Handle snake_case
	if strings.Contains(name, "_") {
		parts := strings.Split(name, "_")
		var result strings.Builder
		for i, part := range parts {
			if len(part) > 0 {
				if i == 0 {
					result.WriteString(strings.ToLower(part[:1]))
					if len(part) > 1 {
						result.WriteString(part[1:])
					}
				} else {
					result.WriteString(strings.ToUpper(part[:1]))
					if len(part) > 1 {
						result.WriteString(part[1:])
					}
				}
			}
		}
		return result.String()
	}

	// Handle kebab-case
	if strings.Contains(name, "-") {
		parts := strings.Split(name, "-")
		var result strings.Builder
		for i, part := range parts {
			if len(part) > 0 {
				if i == 0 {
					result.WriteString(strings.ToLower(part[:1]))
					if len(part) > 1 {
						result.WriteString(part[1:])
					}
				} else {
					result.WriteString(strings.ToUpper(part[:1]))
					if len(part) > 1 {
						result.WriteString(part[1:])
					}
				}
			}
		}
		return result.String()
	}

	// Handle PascalCase -> camelCase
	if len(name) > 0 {
		return strings.ToLower(name[:1]) + name[1:]
	}

	return name
}

// ToClassName converts a name to TypeScript class name (PascalCase)
// Example: "auth" -> "Auth", "my-namespace" -> "MyNamespace"
func ToClassName(name string) string {
	parts := strings.Split(strings.ReplaceAll(name, "_", "-"), "-")
	var result strings.Builder
	for _, part := range parts {
		if len(part) > 0 {
			result.WriteString(strings.ToUpper(part[:1]))
			result.WriteString(part[1:])
		}
	}
	return result.String()
}

// ToSnakeCase converts PascalCase/camelCase to snake_case
func ToSnakeCase(s string) string {
	re := regexp.MustCompile("([a-z0-9])([A-Z])")
	snake := re.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
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

// EscapeString escapes a string for use in TypeScript string literals
func EscapeString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
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
