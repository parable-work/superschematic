package stackconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
)

// CORSSuffix is what the name of an API's CORS field adds to the API's
// name in upper snake case: SHOP_API_CORS is shop-api's (D55).
const CORSSuffix = "_CORS"

// LoadCORS reads the CORS field named field from the environment: the
// origins of the sites that call the API (docs/stack-model.md, section
// 8.10), its one member a comma-separated list:
//
//	field_ORIGINS  the sites' origins, <scheme>://<host>[:<port>] each
//
// An unset or empty field_ORIGINS is no origin: no site calls the API in
// the environment, and its server answers no CORS. An entry that is no
// origin, and a variable under the field's name that is no member, are
// refused.
func LoadCORS(field string) ([]string, error) {
	return loadCORS(field, os.Environ())
}

func loadCORS(field string, environ []string) ([]string, error) {
	var errs []error
	var origins []string
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, field+"_") {
			continue
		}
		if name != field+"_ORIGINS" {
			errs = append(errs, fmt.Errorf("environment variable %s is no member of the CORS field %s", name, field))
			continue
		}
		if value == "" {
			continue
		}
		for _, origin := range strings.Split(value, ",") {
			origin = strings.TrimSpace(origin)
			if err := checkOrigin(origin); err != nil {
				errs = append(errs, fmt.Errorf("environment variable %s: %w", name, err))
				continue
			}
			if !slices.Contains(origins, origin) {
				origins = append(origins, origin)
			}
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return origins, nil
}

// checkOrigin refuses what is no origin a browser sends: an http or https
// URL with a host and nothing after it.
func checkOrigin(origin string) error {
	if origin == "" {
		return errors.New("an entry is empty")
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(origin, "?") || strings.HasSuffix(origin, "#") {
		return fmt.Errorf("%q is no origin: an origin is <scheme>://<host>[:<port>] and nothing more", origin)
	}
	return nil
}
