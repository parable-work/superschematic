// Package names spells superschematic's names as Pulumi and Go names, for
// the provisioner and the binding generator alike.
package names

import (
	"go/token"
	"strings"
	"unicode"
)

// Kebab lowercases a name and joins its words with hyphens: Staging is
// staging, PreProd is pre-prod, HTTPApi is http-api, shop_db is shop-db.
func Kebab(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		switch {
		case r == '_' || r == ' ':
			b.WriteByte('-')
		case unicode.IsUpper(r):
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('-')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// initialisms are the words Go spells in capitals.
var initialisms = map[string]bool{
	"API": true, "CPU": true, "DB": true, "DNS": true, "GCP": true, "HTTP": true, "HTTPS": true,
	"IAM": true, "ID": true, "IP": true, "JSON": true, "KMS": true, "SQL": true, "SSH": true,
	"TCP": true, "TLS": true, "UDP": true, "URI": true, "URL": true, "UUID": true, "VPC": true,
}

// Exported spells a name as an exported Go identifier: shop-api is
// ShopAPI, connectionName is ConnectionName, STRIPE_KEY is StripeKey and
// dnsResourceRecords[0].data is DNSResourceRecords0Data. A name that
// would begin with a digit begins with X.
func Exported(name string) string {
	var b strings.Builder
	for _, word := range words(name) {
		if up := strings.ToUpper(word); initialisms[up] {
			b.WriteString(up)
			continue
		}
		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	out := b.String()
	if out == "" || !unicode.IsLetter([]rune(out)[0]) {
		out = "X" + out
	}
	return out
}

// Unexported spells a name as an unexported Go identifier that is not a
// keyword and does not shadow a name generated code uses: pr is pr,
// pr_number is prNumber, type is type_.
func Unexported(name string) string {
	ws := words(name)
	var b strings.Builder
	for i, word := range ws {
		if i == 0 {
			b.WriteString(strings.ToLower(word))
			continue
		}
		b.WriteString(Exported(word))
	}
	out := b.String()
	if out == "" || !unicode.IsLetter([]rune(out)[0]) {
		out = "x" + out
	}
	if token.IsKeyword(out) || reserved[out] {
		out += "_"
	}
	return out
}

// reserved are the identifiers generated code declares or uses beside a
// parameter.
var reserved = map[string]bool{
	"ctx": true, "opts": true, "ref": true, "err": true, "pulumi": true,
	"any": true, "bool": true, "error": true, "float64": true, "string": true, "nil": true, "true": true, "false": true,
}

// words splits a name at every character that is not a letter or a digit,
// and between a lowercase letter or a digit and an uppercase one, and
// before the last capital of a run followed by a lowercase letter.
func words(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}
