package resolvers

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/registry"
)

// imageSpec is the JSON Schema of an image input's spec.
var imageSpec = []byte(`{
	"type": "object",
	"required": ["dockerfile"],
	"additionalProperties": false,
	"properties": {
		"dockerfile": {"type": "string", "minLength": 1},
		"context": {"type": "string", "minLength": 1}
	}
}`)

// imageInput is an image input's spec: the Dockerfile, from the
// repository root, and the build context, the Dockerfile's directory
// when it is left out and `.` for the repository root.
type imageInput struct {
	Dockerfile string `json:"dockerfile"`
	Context    string `json:"context"`
}

// resolveImage is the `image` resolver. An image resolves to:
//
//   - its Dockerfile, and the ignore file of its context;
//   - each file of the context a `COPY` or an `ADD` names, a directory
//     with all under it, and a pattern with all it matches;
//   - each file of the context a `RUN --mount=type=bind` mounts;
//   - less what the ignore file leaves out of the context, which no
//     instruction can then read.
//
// The ignore file is `<Dockerfile>.dockerignore` when the commit holds
// one, and the context's `.dockerignore` otherwise, as BuildKit chooses.
// Its lines are read as Docker reads them: a pattern from the context's
// root, `*` and `?` within one segment and `**` across them, a pattern
// that matches a directory leaving out all under it, and a line that
// starts with `!` taking files back in, the last matching line deciding.
//
// A source that holds a variable (`COPY ${DIR} /app`) cannot be read
// from the Dockerfile, so the whole context is taken. A source the
// commit does not hold, such as a binary an earlier task builds, is
// reported as generated.
//
// Not read: a `COPY --from` of another stage or image, whose files are
// not the context's; an `ADD` of a URL; and the base images, which a
// Dockerfile holds to their bytes by naming a digest.
func resolveImage(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec imageInput
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	dockerfile, err := cleanDir(spec.Dockerfile)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	if dockerfile == "" || !has(req.Tree, dockerfile) {
		return registry.TaskResolution{}, fmt.Errorf("the commit holds no Dockerfile at %s", spec.Dockerfile)
	}
	context := parentOf(dockerfile)
	if spec.Context != "" {
		if context, err = cleanDir(spec.Context); err != nil {
			return registry.TaskResolution{}, err
		}
	}
	data, err := req.Tree.Read(dockerfile)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	r := newResolution(req.Tree)
	// The ignore file in effect, whose lines leave files out of the
	// context. Both names are the image's when the commit holds them: a
	// change to either may change which is read.
	var rules []ignoreRule
	for _, name := range []string{dockerfile + ".dockerignore", file(context, ".dockerignore")} {
		if !has(req.Tree, name) {
			continue
		}
		if rules == nil {
			ignore, err := req.Tree.Read(name)
			if err != nil {
				return registry.TaskResolution{}, err
			}
			if rules, err = parseDockerignore(string(ignore)); err != nil {
				return registry.TaskResolution{}, fmt.Errorf("%s: %w", name, err)
			}
			if rules == nil {
				rules = []ignoreRule{}
			}
		}
	}
	// take adds a file of the context unless the ignore file leaves it
	// out.
	take := func(p string) {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, context), "/")
		if !ignored(rules, rel) {
			r.paths[p] = true
		}
	}
	takeContext := func() {
		for _, entry := range registry.TaskEntriesUnder(req.Tree.Entries(), context) {
			take(entry.Path)
		}
	}
	for _, source := range dockerSources(string(data)) {
		if source == wholeContext {
			takeContext()
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(source, "/"), "./")
		target, err := joinDir(context, rel, dockerfile)
		if err != nil {
			return registry.TaskResolution{}, err
		}
		if target == context {
			takeContext()
			continue
		}
		matched := matchSources(req.Tree, target)
		if len(matched) == 0 {
			if prefix := literalPrefix(target); prefix != "" && prefix != context {
				r.generate(prefix, dockerfile)
			}
			continue
		}
		for _, p := range matched {
			take(p)
		}
	}
	// The Dockerfile and the ignore files are the image's whatever the
	// ignore file says of them.
	r.add(dockerfile)
	r.add(dockerfile + ".dockerignore")
	r.add(file(context, ".dockerignore"))
	return r.result(), nil
}

// ignoreRule is one line of an ignore file: a pattern, compiled as
// Docker compiles it, and whether the line takes files back in.
type ignoreRule struct {
	match  *regexp.Regexp
	negate bool
}

// parseDockerignore reads an ignore file's lines as Docker does: a blank
// line and a line that starts with `#` say nothing; `!` first takes
// files back in; a pattern is cleaned as a path, and a leading or a
// trailing slash does not matter.
func parseDockerignore(text string) ([]ignoreRule, error) {
	var rules []ignoreRule
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(pattern, "!") {
			rule.negate = true
			pattern = strings.TrimSpace(pattern[1:])
		}
		pattern = strings.TrimPrefix(path.Clean("/"+pattern), "/")
		if pattern == "" {
			// The context's root: every file.
			pattern = "**"
		}
		compiled, err := compileDockerignore(pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", line, err)
		}
		rule.match = compiled
		rules = append(rules, rule)
	}
	return rules, nil
}

// compileDockerignore turns one pattern into the regular expression
// Docker matches paths with: `**/` is any number of directories, `**` at
// the end is anything, `*` is any run of characters within a segment,
// `?` is one of them, and a bracket is a class.
func compileDockerignore(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch ch := runes[i]; ch {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
				}
				if i+1 == len(runes) {
					b.WriteString(".*")
				} else {
					b.WriteString("(.*/)?")
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.', '$', '+', '(', ')', '|', '{', '}', '^':
			b.WriteString(`\` + string(ch))
		case '\\':
			if i+1 < len(runes) {
				i++
				b.WriteString(regexp.QuoteMeta(string(runes[i])))
			}
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ignored reports whether the ignore file leaves a path of the context
// out. Each line is tried in order against the path and against each
// directory above it, and the last line that matches decides.
func ignored(rules []ignoreRule, rel string) bool {
	out := false
	segments := strings.Split(rel, "/")
	for _, rule := range rules {
		matched := false
		for n := len(segments); n >= 1 && !matched; n-- {
			matched = rule.match.MatchString(strings.Join(segments[:n], "/"))
		}
		if matched {
			out = !rule.negate
		}
	}
	return out
}

// matchSources returns the tracked files a source names: the files a
// pattern matches, each directory it matches with all under it.
func matchSources(tree registry.TaskTree, pattern string) []string {
	var out []string
	for _, entry := range registry.TaskEntriesUnder(tree.Entries(), literalPrefix(pattern)) {
		// A source matches a file or a directory by its whole path, so a
		// file under a directory it matches is one of its files.
		segments := strings.Split(entry.Path, "/")
		for n := len(strings.Split(pattern, "/")); n <= len(segments); n++ {
			if matchDir(pattern, strings.Join(segments[:n], "/")) {
				out = append(out, entry.Path)
				break
			}
		}
	}
	return out
}

// wholeContext stands for a source that takes the whole build context.
const wholeContext = "\x00context"

// dockerSources reads a Dockerfile and returns the sources its
// instructions take from the build context: a path or a pattern relative
// to the context, or wholeContext.
func dockerSources(text string) []string {
	var sources []string
	for _, instruction := range dockerInstructions(text) {
		name, rest, _ := strings.Cut(instruction, " ")
		switch strings.ToUpper(name) {
		case "COPY", "ADD":
			sources = append(sources, copySources(rest)...)
		case "RUN":
			sources = append(sources, mountSources(rest)...)
		}
	}
	return sources
}

// dockerInstructions splits a Dockerfile into its instructions: a line
// that ends with the escape character runs on into the next, a comment
// line is dropped, and the body of a here-document is skipped.
func dockerInstructions(text string) []string {
	escape := `\`
	var out []string
	var current strings.Builder
	var heredocs []string
	directives := true
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if len(heredocs) > 0 {
			if strings.TrimSpace(line) == heredocs[0] {
				heredocs = heredocs[1:]
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A parser directive may set the escape character, before
			// any instruction.
			if value, ok := strings.CutPrefix(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(trimmed[1:])), " ", ""), "escape="); ok && directives && len(value) == 1 {
				escape = value
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		directives = false
		if continued, ok := strings.CutSuffix(trimmed, escape); ok {
			current.WriteString(continued)
			current.WriteString(" ")
			continue
		}
		current.WriteString(trimmed)
		instruction := current.String()
		current.Reset()
		out = append(out, instruction)
		heredocs = heredocTerminators(instruction)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// heredocTerminators returns the words that end the here-documents an
// instruction opens (`<<EOF`, `<<-EOF`, `<<"EOF"`), in order.
func heredocTerminators(instruction string) []string {
	var out []string
	for _, word := range strings.Fields(instruction) {
		rest, ok := strings.CutPrefix(word, "<<")
		if !ok {
			continue
		}
		rest = strings.Trim(strings.TrimPrefix(rest, "-"), `"'`)
		if rest != "" && !strings.ContainsAny(rest, "<>|&;") {
			out = append(out, rest)
		}
	}
	return out
}

// copySources reads the arguments of a COPY or an ADD: its flags, then
// its sources and a destination, as words or as a JSON list.
func copySources(rest string) []string {
	rest = strings.TrimSpace(rest)
	fromOtherStage := false
	for strings.HasPrefix(rest, "--") {
		flag, remainder, _ := strings.Cut(rest, " ")
		if strings.HasPrefix(flag, "--from=") {
			fromOtherStage = true
		}
		rest = strings.TrimSpace(remainder)
	}
	if fromOtherStage {
		return nil
	}
	var words []string
	if strings.HasPrefix(rest, "[") {
		if json.Unmarshal([]byte(rest), &words) != nil {
			return []string{wholeContext}
		}
	} else {
		words = strings.Fields(rest)
	}
	if len(words) < 2 {
		return nil
	}
	var out []string
	for _, word := range words[:len(words)-1] {
		if source, ok := contextSource(word); ok {
			out = append(out, source)
		}
	}
	return out
}

// contextSource reads one source word: a path of the context, the whole
// context for a word that holds a variable, and nothing for a URL or a
// here-document.
func contextSource(word string) (string, bool) {
	word = strings.Trim(word, `"'`)
	switch {
	case word == "" || strings.HasPrefix(word, "<<"):
		return "", false
	case strings.Contains(word, "://") || strings.HasPrefix(word, "git@"):
		return "", false
	case strings.Contains(word, "$"):
		return wholeContext, true
	case word == "." || word == "./" || word == "/":
		return wholeContext, true
	}
	return strings.TrimSuffix(word, "/"), true
}

// mountSources reads the `--mount` flags of a RUN. A bind mount with no
// `from` mounts the context: the path its `source` names, or all of it.
// A mount with no `type` is a bind mount.
func mountSources(rest string) []string {
	var out []string
	for _, word := range strings.Fields(rest) {
		options, ok := strings.CutPrefix(word, "--mount=")
		if !ok {
			if !strings.HasPrefix(word, "--") {
				break
			}
			continue
		}
		kind, source, from := "bind", "", false
		for _, option := range strings.Split(options, ",") {
			key, value, _ := strings.Cut(option, "=")
			switch key {
			case "type":
				kind = value
			case "source", "src":
				source = value
			case "from":
				from = true
			}
		}
		if kind != "bind" || from {
			continue
		}
		if source == "" {
			out = append(out, wholeContext)
			continue
		}
		if read, ok := contextSource(source); ok {
			out = append(out, read)
		}
	}
	return out
}
