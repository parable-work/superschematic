package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// The pure parts of the orchestration, shared with the TypeScript and Rust
// runtimes through runtime/http/testdata/agents_parity.json.

// The classes of operation an autonomy policy names (D158).
const (
	Read          = "read"
	InternalWrite = "internalWrite"
	MessageParty  = "messageParty"
	MoneyMovement = "moneyMovement"
)

// The autonomy an agent has over an operation.
const (
	Auto = "auto"
	Tell = "tell"
	Ask  = "ask"
)

// The trust of an inbound message's run (D157, D183).
const (
	Verified = "verified"
	Entered  = "entered"
)

// DefaultAutonomy is the default autonomy of a class: a read and an
// internal write run; a message to a party and a money movement ask.
func DefaultAutonomy(class string) string {
	if class == Read || class == InternalWrite {
		return Auto
	}
	return Ask
}

// TrustOf is the run's trust of a message's recorded trust: verified only
// when the row says so, entered for anything else, an identity entered by
// hand, a trust the row does not record, a message the dispatcher could
// not read.
func TrustOf(recorded any) string {
	if s, ok := recorded.(string); ok && s == Verified {
		return Verified
	}
	return Entered
}

// EffectiveAutonomy is the autonomy a run has over an operation of class
// (D158, D183): the policy's mode, but a money movement asks whatever it
// says, and so does every operation but a read of a run whose sender was
// entered by hand. trust is "" for a run no inbound message started.
func EffectiveAutonomy(mode, class, trust string) string {
	if class == MoneyMovement {
		return Ask
	}
	if trust == Entered && class != Read {
		return Ask
	}
	return mode
}

// NarrowReaches is the relation paths a run reaches from a target it
// holds, narrowed to those of the party it acts for (D183): the role's
// paths the party's grants of the same target also reach, by type, each
// once. A target no grant of the party names gives no path.
func NarrowReaches(role map[string][]string, party []map[string][]string) map[string][]string {
	out := map[string][]string{}
	for typeName, paths := range role {
		var kept []string
		for _, path := range paths {
			if contains(kept, path) {
				continue
			}
			for _, reach := range party {
				if contains(reach[typeName], path) {
					kept = append(kept, path)
					break
				}
			}
		}
		if len(kept) > 0 {
			out[typeName] = kept
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// NarrowPermissions is the agent's permissions narrowed to those whom it
// acts for holds (D158): each of the agent's that one of the actor's
// covers, then each of the actor's that one of the agent's covers, the
// narrower of a pair, each once.
func NarrowPermissions(agent, actor []string) []string {
	out := []string{}
	add := func(p string) {
		if !contains(out, p) {
			out = append(out, p)
		}
	}
	for _, p := range agent {
		for _, q := range actor {
			if session.Covers(q, p) {
				add(p)
				break
			}
		}
	}
	for _, q := range actor {
		for _, p := range agent {
			if session.Covers(p, q) {
				add(q)
				break
			}
		}
	}
	return out
}

// PolicyRow is an autonomy policy row as BestPolicy reads it: "" for a
// member the row leaves null.
type PolicyRow struct {
	Agent          string `json:"agent"`
	Operation      string `json:"operation"`
	OperationClass string `json:"operationClass"`
	Mode           string `json:"mode"`
	Permission     string `json:"permission"`
}

// BestPolicy is the index of the most specific of rows, the policies of an
// agent and of every agent, for operation of class (D158): one of the
// agent's over one of every agent's, one naming the operation over one
// naming its class, the first of equals in the order given; -1 when no
// row names either.
func BestPolicy(rows []PolicyRow, operation, class string) int {
	score := func(row PolicyRow) int {
		n := 0
		if row.Agent != "" {
			n += 4
		}
		switch {
		case row.Operation == operation:
			n += 2
		case row.Operation != "":
			n -= 100
		case row.OperationClass == class:
			n++
		default:
			n -= 100
		}
		return n
	}
	best, at := -1, -1
	for i, row := range rows {
		if s := score(row); s >= 0 && s > best {
			best, at = s, i
		}
	}
	return at
}

// BudgetCrossing is what a use that took a budget's meter from before to
// after crossed of a budget of limit (nil for none) that warns at warnAt
// percent (D158): a warning as it crosses the threshold, once, and the cap
// at the limit.
func BudgetCrossing(before, after int64, limit *int64, warnAt int64) (warns, capped bool) {
	if limit == nil {
		return false, false
	}
	warns = *limit > 0 && before*100 < warnAt**limit && after*100 >= warnAt**limit
	return warns, after >= *limit
}

// redacted is what a redaction leaves in place of what it takes out.
const redacted = "[redacted]"

// RedactTokens is text redacted as a transcript entry is (D158): each of
// secrets taken out where it appears, then every grant's token
// (grant_ and 16 or more of [A-Za-z0-9_-]) and every bearer credential
// (Bearer, white space, and [A-Za-z0-9._~+/=-]), then each of extra, each
// replaced by [redacted]. The runtime has no regular expressions to keep
// for two patterns, so they are written out, as Rust's redact_text is
// (D193); the app's own redactions are texts.
func RedactTokens(text string, secrets []string, extra []string) string {
	out := text
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, redacted)
		}
	}
	out = redactGrants(out)
	out = redactBearers(out)
	for _, more := range extra {
		if more != "" {
			out = strings.ReplaceAll(out, more, redacted)
		}
	}
	return out
}

func grantChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func bearerChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("._~+/=-", c) >= 0
}

func redactGrants(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "grant_") {
			j := i + len("grant_")
			for j < len(text) && grantChar(text[j]) {
				j++
			}
			if j-i-len("grant_") >= 16 {
				b.WriteString(redacted)
				i = j
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// jsSpace reports whether r is white space as a JavaScript pattern's \s
// reads it: Unicode's, U+FEFF among them and U+0085 not.
func jsSpace(r rune) bool {
	if r == 0x85 {
		return false
	}
	return r == 0xfeff || unicode.IsSpace(r)
}

func redactBearers(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "Bearer") {
			j := i + len("Bearer")
			spaces := 0
			for j < len(text) {
				r, size := utf8.DecodeRuneInString(text[j:])
				if !jsSpace(r) {
					break
				}
				j += size
				spaces++
			}
			k := j
			for k < len(text) && bearerChar(text[k]) {
				k++
			}
			if spaces > 0 && k > j {
				b.WriteString(redacted)
				i = k
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// CanonicalJSON is v as canonical JSON: its object members sorted by name,
// no white space, no HTML escaped, as TypeScript's canonicalJson writes it.
func CanonicalJSON(v any) string {
	normal := normalise(v)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normal); err != nil {
		return "null"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// normalise is v read back from its JSON, numbers kept as written, so a
// struct and a map print alike and members sort by name.
func normalise(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out any
	if decoder.Decode(&out) != nil {
		return nil
	}
	return out
}

// ParamsHash is the SHA-256, in hex, of an operation and its parameters as
// canonical JSON: what an approval is bound to.
func ParamsHash(operation string, params any) string {
	sum := sha256.Sum256([]byte(CanonicalJSON(map[string]any{"operation": operation, "params": params})))
	return hex.EncodeToString(sum[:])
}
