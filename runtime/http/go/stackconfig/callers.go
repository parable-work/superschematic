package stackconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// CallersSuffix is what the name of an API's callers field adds to the
// API's name in upper snake case: SHOP_API_CALLERS is shop-api's.
const CallersSuffix = "_CALLERS"

// maxCount bounds a list's length variable, so a mistaken value cannot
// make the loader read an unbounded number of variables.
const maxCount = 1000

// LoadCallers reads the callers field named field from the environment:
// what the server of an API with a service clause verifies a caller's
// service credential against (docs/stack-model.md, sections 3.4 and 9.2).
// Its value is an ir.ServiceAuth, one variable per member, a list of
// objects as a variable holding its length and each object's members
// under its index:
//
//	field_ISSUERS                          the number of issuers
//	field_ISSUERS_<i>_ISSUER               the iss the issuer writes
//	field_ISSUERS_<i>_ISSUER_ALIASES       other iss values, comma-separated
//	field_ISSUERS_<i>_AUDIENCE             the aud a token must hold
//	field_ISSUERS_<i>_ALGORITHMS           RS256, ES256 or EdDSA, comma-separated
//	field_ISSUERS_<i>_JWKS_URL             where the issuer's keys are, or
//	field_ISSUERS_<i>_KEYS                 the number of keys, each
//	field_ISSUERS_<i>_KEYS_<j>_JWK         a public JWK as JSON
//	field_ISSUERS_<i>_SUBJECT_CLAIM        the claim that names the caller
//	field_ISSUERS_<i>_MAX_LIFETIME_SECONDS the longest a token may live
//	field_ISSUERS_<i>_CALLERS              the number of callers, each
//	field_ISSUERS_<i>_CALLERS_<k>_SUBJECT     the claim's value
//	field_ISSUERS_<i>_CALLERS_<k>_DEPLOYABLE  the deployable it is
//	field_ISSUERS_<i>_CALLERS_<k>_SERVES      the APIs it serves, comma-separated
//
// It returns the field as the serviceauth.Config a serviceauth.Verifier
// takes, each issuer's callers keyed by subject. No issuers means no
// server calls the API in the environment: the verifier then refuses
// every service credential. A variable under the field's name that is no
// member, or a list whose length its variables do not match, is refused.
func LoadCallers(field string) (serviceauth.Config, error) {
	return loadCallers(field, os.Environ())
}

// callerVars reads a callers field's variables and records which it read.
type callerVars struct {
	vars map[string]string
	read map[string]bool
	errs []error
}

func (c *callerVars) get(name string) (string, bool) {
	c.read[name] = true
	value, ok := c.vars[name]
	return value, ok
}

func (c *callerVars) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf(format, args...))
}

// required reads a variable that must be set and not empty.
func (c *callerVars) required(name string) string {
	value, ok := c.get(name)
	if !ok || value == "" {
		c.fail("required environment variable %s is not set", name)
	}
	return value
}

// count reads a list's length.
func (c *callerVars) count(name string, least int) int {
	value := c.required(name)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < least || n > maxCount {
		c.fail("environment variable %s is %q; want a number of entries from %d to %d", name, value, least, maxCount)
		return 0
	}
	return n
}

// list reads a comma-separated list, which must not be empty when set.
func (c *callerVars) list(name string, required bool) []string {
	value, ok := c.get(name)
	if !ok || value == "" {
		if required {
			c.fail("required environment variable %s is not set", name)
		}
		return nil
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			c.fail("environment variable %s has an empty entry", name)
			continue
		}
		out = append(out, item)
	}
	return out
}

func loadCallers(field string, environ []string) (serviceauth.Config, error) {
	c := &callerVars{vars: map[string]string{}, read: map[string]bool{}}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, field+"_") {
			c.vars[name] = value
		}
	}
	cfg := serviceauth.Config{Issuers: []serviceauth.IssuerConfig{}}
	issuers := c.count(field+"_ISSUERS", 0)
	for i := range issuers {
		cfg.Issuers = append(cfg.Issuers, c.issuer(fmt.Sprintf("%s_ISSUERS_%d_", field, i)))
	}
	var unknown []string
	for name := range c.vars {
		if !c.read[name] {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		c.fail("environment variable %s is no member of the callers field %s", name, field)
	}
	if len(c.errs) > 0 {
		return serviceauth.Config{}, errors.Join(c.errs...)
	}
	return cfg, nil
}

// issuer reads the issuer whose variables begin with p.
func (c *callerVars) issuer(p string) serviceauth.IssuerConfig {
	issuer := serviceauth.IssuerConfig{
		Issuer:        c.required(p + "ISSUER"),
		IssuerAliases: c.list(p+"ISSUER_ALIASES", false),
		Audience:      c.required(p + "AUDIENCE"),
		Algorithms:    c.list(p+"ALGORITHMS", true),
		Callers:       map[string]serviceauth.CallerConfig{},
	}
	issuer.JWKSURL, _ = c.get(p + "JWKS_URL")
	if _, ok := c.get(p + "KEYS"); ok {
		for j := range c.count(p+"KEYS", 1) {
			name := fmt.Sprintf("%sKEYS_%d_JWK", p, j)
			text := c.required(name)
			if text == "" {
				continue
			}
			var key serviceauth.JWK
			if err := json.Unmarshal([]byte(text), &key); err != nil {
				c.fail("environment variable %s is not a JWK: %v", name, err)
				continue
			}
			issuer.Keys = append(issuer.Keys, key)
		}
	}
	issuer.SubjectClaim, _ = c.get(p + "SUBJECT_CLAIM")
	if value, ok := c.get(p + "MAX_LIFETIME_SECONDS"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 {
			c.fail("environment variable %sMAX_LIFETIME_SECONDS is %q; want a whole number of seconds above 0", p, value)
		}
		issuer.MaxLifetimeSeconds = n
	}
	for k := range c.count(p+"CALLERS", 1) {
		q := fmt.Sprintf("%sCALLERS_%d_", p, k)
		subject := c.required(q + "SUBJECT")
		caller := serviceauth.CallerConfig{Deployable: c.required(q + "DEPLOYABLE"), Serves: c.list(q+"SERVES", true)}
		if _, dup := issuer.Callers[subject]; dup && subject != "" {
			c.fail("environment variable %sSUBJECT is %s, another caller's of the issuer %s", q, subject, issuer.Issuer)
			continue
		}
		issuer.Callers[subject] = caller
	}
	return issuer
}
