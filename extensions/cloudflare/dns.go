package cloudflare

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TypeRecord is the resource type the DNS platform emits: a DNS record of
// pulumi-cloudflare 6, which renamed `cloudflare:index/record:Record`
// (Terraform's `cloudflare_record`) to it. It is pinned in schemas/.
const TypeRecord = "cloudflare:index/dnsRecord:DnsRecord"

// TokenEnv is the environment variable the provider reads its API token
// from. A run sets it from the secret TokenSecret names; the token never
// enters the resource graph, the rendered program or the stack's state
// configuration.
const TokenEnv = "CLOUDFLARE_API_TOKEN"

const (
	// recordTTL is the TTL of a DNS-only record, as Cloud DNS writes its
	// records.
	recordTTL = 300

	// automaticTTL is Cloudflare's "automatic" TTL, which a proxied record
	// always has.
	automaticTTL = 1
)

// dnsValues is the schema of an environment's Cloudflare DNS values:
//
//   - zone, the name of the Cloudflare zone that holds the domain
//     (`acme.dev` for `staging.acme.dev`);
//   - zoneId, the zone's identifier, which the record resource takes. It is
//     on the zone's Overview page in the dashboard, and is not a secret.
//     The graph has no function call to look it up by name, and a lookup
//     at deploy time would make the plan depend on the API;
//   - proxied, whether the hosts are proxied through Cloudflare. Records
//     are DNS-only by default.
const dnsValues = `{
  "type": "object",
  "required": ["zone", "zoneId"],
  "properties": {
    "zone": {"type": "string", "pattern": "^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\\.)+[a-z][-a-z0-9]{0,61}[a-z0-9]$"},
    "zoneId": {"type": "string", "pattern": "^[0-9a-f]{32}$"},
    "proxied": {"type": "boolean"}
  },
  "additionalProperties": false
}`

// values are an environment's Cloudflare DNS values, which resolution has
// checked against dnsValues.
type values struct {
	zone    string
	zoneID  string
	proxied bool
}

func valuesOf(m map[string]any) values {
	var v values
	v.zone, _ = m["zone"].(string)
	v.zoneID, _ = m["zoneId"].(string)
	v.proxied, _ = m["proxied"].(bool)
	return v
}

// TokenSecret returns the name of the secret that holds a stack's API
// token for a zone: the stack, `cloudflare-dns` and the zone with its dots
// as underscores, joined with hyphens (`shop-cloudflare-dns-acme_dev`). A
// zone's name holds no underscore, so two zones never share a secret, and
// the name is a valid Secret Manager secret id. The environments of a
// stack whose records share a zone share the token.
func TokenSecret(stack, zone string) string {
	return stack + "-cloudflare-dns-" + strings.ReplaceAll(zone, ".", "_")
}

// credentials names the API token the provider reads: one scoped to the
// zone's DNS, which bootstrap asks for.
func credentials(ctx registry.DNSContext) []ir.DNSCredential {
	zone := valuesOf(ctx.Values).zone
	return []ir.DNSCredential{{
		Secret:      TokenSecret(ctx.Environment.Stack, zone),
		Env:         TokenEnv,
		Description: fmt.Sprintf("a Cloudflare API token with the DNS Edit permission on zone %s, and no other", zone),
	}}
}

// lowerRecords is the Cloudflare DNS platform (section 6.9): it writes
// each record into the zone the values name, as a record named by its full
// name. It writes A, AAAA, CNAME and TXT records, the types exposure
// produces, the records a certificate needs for validation among them.
//
// With proxied set, a host's A, AAAA and CNAME records are proxied through
// Cloudflare, with the automatic TTL a proxied record has. A record whose
// name begins with an underscore label (`_acme-challenge`) is never
// proxied: it is not a host, and a proxied validation record would answer
// with Cloudflare's addresses instead of its value.
//
// A TXT record's content is written as RFC 1035 character strings, in
// double quotes of at most 255 bytes each, as Cloudflare stores it. A
// trailing dot is dropped from a name and from a CNAME's literal content,
// since Cloudflare stores names without it. The zone itself is not created
// here: it holds the domain before the stack does.
func lowerRecords(ctx registry.DNSContext) ([]*ir.Resource, error) {
	v := valuesOf(ctx.Values)
	if !inZone(ctx.Environment.Domain, v.zone) {
		return nil, fmt.Errorf("environment %s has domain %s, which is not in zone %s", ctx.Environment.Name, ctx.Environment.Domain, v.zone)
	}
	count := map[string]int{}
	var out []*ir.Resource
	for _, rec := range ctx.Records {
		name := trimDot(rec.Name)
		content := rec.Value
		switch rec.Type {
		case "A", "AAAA":
		case "CNAME":
			content = trimDot(content)
		case "TXT":
			content = txtContent(content)
		default:
			return nil, fmt.Errorf("record %s of %s has type %s; the cloudflare DNS platform writes A, AAAA, CNAME and TXT records", describe(rec.Name), rec.Deployable, rec.Type)
		}
		if !inZone(name, v.zone) {
			return nil, fmt.Errorf("record %s of %s is not in zone %s", describe(rec.Name), rec.Deployable, v.zone)
		}
		proxied := v.proxied && rec.Type != "TXT" && !underscored(name)
		ttl := recordTTL
		if proxied {
			ttl = automaticTTL
		}
		key := rec.Deployable + "." + strings.ToLower(rec.Type)
		count[key]++
		id := "dns." + key
		if count[key] > 1 {
			id = fmt.Sprintf("%s.%d", id, count[key])
		}
		out = append(out, &ir.Resource{ID: id, Type: TypeRecord, Properties: map[string]any{
			"zoneId":  v.zoneID,
			"name":    name,
			"type":    rec.Type,
			"content": content,
			"ttl":     ttl,
			"proxied": proxied,
		}})
	}
	return out, nil
}

// inZone reports whether a name is the zone or a name under it. A name
// that ends in a reference cannot be checked offline, and passes; one that
// ends in a string is checked by that string.
func inZone(name any, zone string) bool {
	switch n := name.(type) {
	case string:
		n = strings.ToLower(n)
		return n == zone || strings.HasSuffix(n, "."+zone)
	case ir.Concat:
		if len(n) > 0 {
			if tail, ok := n[len(n)-1].(string); ok {
				return strings.HasSuffix(strings.ToLower(tail), "."+zone)
			}
		}
	}
	return true
}

// underscored reports whether a name's first label begins with an
// underscore, as a validation or service label does. A name that begins
// with a reference does not.
func underscored(name any) bool {
	switch n := name.(type) {
	case string:
		return strings.HasPrefix(n, "_")
	case ir.Concat:
		if len(n) > 0 {
			head, ok := n[0].(string)
			return ok && strings.HasPrefix(head, "_")
		}
	}
	return false
}

// trimDot drops a trailing dot from a string, or from a concat that ends
// in one. A reference stays as it is.
func trimDot(v any) any {
	switch v := v.(type) {
	case string:
		return strings.TrimSuffix(v, ".")
	case ir.Concat:
		if len(v) > 0 {
			if tail, ok := v[len(v)-1].(string); ok && strings.HasSuffix(tail, ".") {
				out := append(ir.Concat(nil), v...)
				out[len(out)-1] = strings.TrimSuffix(tail, ".")
				return join(out...)
			}
		}
	}
	return v
}

// txtContent writes a TXT record's value as character strings. A literal
// that already begins with a double quote is taken as written; any other
// is escaped and split into strings of at most 255 bytes, at character
// boundaries. A value that references an output is quoted whole.
func txtContent(v any) any {
	s, ok := v.(string)
	if !ok {
		return join(`"`, v, `"`)
	}
	if strings.HasPrefix(s, `"`) {
		return s
	}
	var parts []string
	for {
		n := len(s)
		if n > 255 {
			n = 255
			for n > 0 && !utf8.RuneStart(s[n]) {
				n--
			}
		}
		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s[:n])
		parts = append(parts, `"`+escaped+`"`)
		if s = s[n:]; s == "" {
			break
		}
	}
	return strings.Join(parts, " ")
}

// join joins strings and references into one value: a string when every
// part is a string, else an ir.Concat with adjacent strings merged.
func join(parts ...any) any {
	var out ir.Concat
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	var add func(any)
	add = func(part any) {
		switch part := part.(type) {
		case string:
			buf.WriteString(part)
		case ir.Concat:
			for _, inner := range part {
				add(inner)
			}
		default:
			flush()
			out = append(out, part)
		}
	}
	for _, part := range parts {
		add(part)
	}
	flush()
	if len(out) == 1 {
		if s, ok := out[0].(string); ok {
			return s
		}
	}
	if len(out) == 0 {
		return ""
	}
	return out
}

// describe spells a record's name for an error: a string as it is, a
// value with references in its JSON form.
func describe(name any) string {
	if s, ok := name.(string); ok {
		return s
	}
	data, err := json.Marshal(name)
	if err != nil {
		return fmt.Sprint(name)
	}
	return string(data)
}
