package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// numberingBehavior is the name of the core's Numbering, which a table
// carries on a field (D56, D103's step 4).
const numberingBehavior = "Numbering"

// numberingTokens names the tokens a Numbering format may hold, in the
// core's words.
const numberingTokens = "{seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}"

// checkNumberingConfig holds the config of a Numbering use on a field of
// the table td to the table (D103's step 4), in the core's words and order
// (superschematic_rules_behaviors::numbering): the format holds {seq} or
// {seq:N} once and no brace outside a token; a yearly reset prints the
// year; and scope.link names a to-one relation of td, which the build also
// holds required, since a row with no target has no count to be numbered
// in. Each problem is `type <T>: field <f>: behavior Numbering config:
// <reason>`, and like the core it reports the first. A config the
// configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkNumberingConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Format string `json:"format"`
		Scope  *struct {
			Link string `json:"link"`
		} `json:"scope"`
		Reset string `json:"reset"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := numberingProblem(schema, td, config.Format, config.Scope != nil, scopeLink(config.Scope), config.Reset == "yearly"); why != "" {
		r.errorf(td.Owner, "type %s: field %s: behavior %s config: %s", td.Name, ref.Field, ref.Name, why)
	}
}

// scopeLink is a scope's link, "" for none.
func scopeLink(scope *struct {
	Link string `json:"link"`
}) string {
	if scope == nil {
		return ""
	}
	return scope.Link
}

// numberingProblem is the first problem of a table's Numbering config, or
// "".
func numberingProblem(schema *ir.Schema, td *ir.TypeDef, format string, scoped bool, link string, yearly bool) string {
	dated, why := numberingFormat(format)
	if why != "" {
		return why
	}
	if yearly && !dated {
		return fmt.Sprintf("reset yearly counts each year from 1, so format %q prints the year, {yyyy} or {yy}, or two years would print one label", format)
	}
	if !scoped {
		return ""
	}
	var relations []string
	var found *ir.FieldDef
	for _, fd := range td.Fields {
		if fd == nil || fd.Owned() || fd.TypeRef.IsArray || !sqlutil.IsTableRelation(schema, fd) {
			continue
		}
		relations = append(relations, fd.Name)
		if fd.Name == link {
			found = fd
		}
	}
	if found == nil {
		listed := "it has none"
		if len(relations) > 0 {
			listed = "its to-one relations: " + strings.Join(relations, ", ")
		}
		return fmt.Sprintf("scope.link %s is not a to-one relation of %s (%s), so a row has no one target to count by", link, td.Name, listed)
	}
	if !found.Required {
		return fmt.Sprintf("scope.link %s is not required, so a row may hold no target to count by; make it required", link)
	}
	return ""
}

// numberingFormat reads a format as the core does: whether it prints the
// year, or why the core refuses it, in its words.
func numberingFormat(format string) (dated bool, why string) {
	seqs := 0
	for at := 0; at < len(format); {
		c := format[at]
		if c != '{' && c != '}' {
			at++
			continue
		}
		end := -1
		if c == '{' {
			if next := strings.IndexAny(format[at+1:], "{}"); next >= 0 && format[at+1+next] == '}' {
				end = at + 1 + next
			}
		}
		if end < 0 {
			return false, fmt.Sprintf("format %q has a %q that is not part of a token; its tokens are %s", format, string(c), numberingTokens)
		}
		token := format[at+1 : end]
		at = end + 1
		switch {
		case token == "yyyy" || token == "yy":
			dated = true
		case seqToken(token):
			seqs++
		default:
			return false, fmt.Sprintf("format %q has {%s}, which is not one of its tokens: %s", format, token, numberingTokens)
		}
	}
	switch {
	case seqs == 0:
		return false, fmt.Sprintf("format %q has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once", format)
	case seqs > 1:
		return false, fmt.Sprintf("format %q has the number %d times: it prints it with {seq} or {seq:N} once, so two numbers never print one label", format, seqs)
	}
	return dated, ""
}

// seqToken reports whether a token is seq, or seq:N with N from 1 to 10
// written without a leading zero.
func seqToken(token string) bool {
	if token == "seq" {
		return true
	}
	digits, ok := strings.CutPrefix(token, "seq:")
	if !ok || len(digits) == 0 || len(digits) > 2 || digits[0] == '0' || slices.ContainsFunc([]byte(digits), func(b byte) bool { return b < '0' || b > '9' }) {
		return false
	}
	n, err := strconv.Atoi(digits)
	return err == nil && n >= 1 && n <= 10
}
