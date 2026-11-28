package registry

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// identityRouteDecorators registers the user model's route sets (D50):
// @userSessions and @userAdministration from @superschematic/api, each on
// a class with no methods, which the walker reads as an operation set. A
// class takes one of the two, once. The loader adds the set's operations;
// the rules that read the whole schema or its authDb are the verification
// pass's.
func identityRouteDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "userSessions", Packages: []string{pkgAPI}, Target: TargetOperationSet,
			Apply: func(n Node, args []any, _ Site) error { return applyUserSessions(args, n.OperationSet) },
		},
		{
			Name: "userAdministration", Packages: []string{pkgAPI}, Target: TargetOperationSet,
			Apply: func(n Node, args []any, _ Site) error { return applyUserAdministration(args, n.OperationSet) },
		},
	}
}

// applyUserSessions reads @userSessions({ path?, login?, register?,
// registration?, defaultRoles?, firstUser?, apiKeys?, oauth? }). login is true and
// register false when absent, and register needs the login; registration,
// defaultRoles and firstUser (D81) need register. apiKeys (D114), false
// when absent, needs the login too: a key is made by a caller with a
// session. oauth (#695) needs the login too: a person signs in to consent.
func applyUserSessions(args []any, set *ir.OperationSet) error {
	const name = "userSessions"
	if err := identityRoutesDeclared(name, set); err != nil {
		return err
	}
	cfg := &ir.UserSessionsConfig{}
	login, register, apiKeys := true, false, false
	var registrationKeys []string
	err := identityRoutesConfig(name, args, map[string]func(any) error{
		"path": func(v any) error { return identityRoutesPath(name, v, &cfg.Path) },
		"login": func(v any) error {
			return identityRoutesFlag(name, "login", v, &login)
		},
		"register": func(v any) error {
			return identityRoutesFlag(name, "register", v, &register)
		},
		"registration": func(v any) error {
			registrationKeys = append(registrationKeys, "registration")
			mode, ok := v.(string)
			if !ok || (mode != ir.IdentityRegistrationOpen && mode != ir.IdentityRegistrationApproval) {
				return ArgErrorf(0, "@userSessions registration must be %q or %q", ir.IdentityRegistrationOpen, ir.IdentityRegistrationApproval)
			}
			if mode == ir.IdentityRegistrationApproval {
				cfg.Registration = mode
			}
			return nil
		},
		"defaultRoles": func(v any) error {
			registrationKeys = append(registrationKeys, "defaultRoles")
			roles, err := identityRoutesStrings(name, "defaultRoles", v)
			cfg.DefaultRoles = roles
			return err
		},
		"firstUser": func(v any) error {
			registrationKeys = append(registrationKeys, "firstUser")
			first, err := identityRoutesFirstUser(v)
			cfg.FirstUser = first
			return err
		},
		"apiKeys": func(v any) error {
			return identityRoutesFlag(name, "apiKeys", v, &apiKeys)
		},
		"oauth": func(v any) error {
			oauth, err := identityRoutesOAuth(v)
			cfg.OAuth = oauth
			return err
		},
	})
	if err != nil {
		return err
	}
	if register && !login {
		return ArgErrorf(0, "@userSessions: register needs the login; drop register: true or login: false")
	}
	if len(registrationKeys) > 0 && !register {
		verb := "configures register, so it needs"
		if len(registrationKeys) > 1 {
			verb = "configure register, so they need"
		}
		return ArgErrorf(0, "@userSessions: %s %s register: true", joinKeys(registrationKeys), verb)
	}
	if apiKeys && !login {
		return ArgErrorf(0, "@userSessions: apiKeys needs the login, since a key is made by a caller signed in with a session; drop apiKeys: true or login: false")
	}
	if cfg.OAuth != nil && !login {
		return ArgErrorf(0, "@userSessions: oauth needs the login, since a person signs in to consent; drop oauth or login: false")
	}
	cfg.NoLogin, cfg.Register, cfg.APIKeys = !login, register, apiKeys
	set.UserSessions = cfg
	return nil
}

// identityRoutesStrings reads a list of non-empty strings with no repeats.
func identityRoutesStrings(name, key string, v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, ArgErrorf(0, "@%s %s must be a list of strings", name, key)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		switch {
		case !ok:
			return nil, ArgErrorf(0, "@%s %s must be a list of strings", name, key)
		case text == "":
			return nil, ArgErrorf(0, "@%s %s holds an empty string", name, key)
		case slices.Contains(out, text):
			return nil, ArgErrorf(0, "@%s %s names %q twice", name, key, text)
		}
		out = append(out, text)
	}
	return out, nil
}

// identityRoutesFirstUser reads firstUser: { role, permissions }, the role
// a non-empty string and the permissions a non-empty list of permissions.
func identityRoutesFirstUser(v any) (*ir.IdentityFirstUser, error) {
	const name = "userSessions"
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@userSessions firstUser must be an object literal: { role, permissions }")
	}
	first := &ir.IdentityFirstUser{}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := obj[key]
		switch key {
		case "role":
			role, ok := value.(string)
			if !ok || role == "" {
				return nil, ArgErrorf(0, "@userSessions firstUser.role must be a non-empty string, the name of the administrator's role")
			}
			first.Role = role
		case "permissions":
			permissions, err := identityRoutesStrings(name, "firstUser.permissions", value)
			if err != nil {
				return nil, err
			}
			first.Permissions = permissions
		default:
			return nil, ArgErrorf(0, "@userSessions firstUser has unknown key %q; it takes role and permissions", key)
		}
	}
	switch {
	case first.Role == "":
		return nil, ArgErrorf(0, "@userSessions firstUser needs role, the name of the administrator's role")
	case len(first.Permissions) == 0:
		return nil, ArgErrorf(0, "@userSessions firstUser needs permissions, at least one, which the role carries when register creates it")
	}
	return first, nil
}

// identityRoutesOAuth reads oauth (#695): true for every default, false
// for none, or { accessTokenTtlSeconds?, refreshTokenTtlSeconds?,
// grantMaxTtlSeconds?, clients?: { metadataDocuments?, registration? },
// consentPath? }. Each lifetime is a positive whole number of seconds, an
// access token's shorter than a refresh token's and a refresh token's no
// longer than the grant's, and the two kinds of client are not both off.
func identityRoutesOAuth(v any) (*ir.IdentityOAuthConfig, error) {
	if flag, ok := v.(bool); ok {
		if !flag {
			return nil, nil
		}
		return &ir.IdentityOAuthConfig{}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@userSessions oauth must be true or an object literal: { accessTokenTtlSeconds?, refreshTokenTtlSeconds?, grantMaxTtlSeconds?, clients?, consentPath? }")
	}
	cfg := &ir.IdentityOAuthConfig{}
	seconds := func(key string, value any, dst *int64) error {
		n, ok := value.(float64)
		if !ok {
			if i, isInt := value.(int); isInt {
				n, ok = float64(i), true
			} else if i, isInt := value.(int64); isInt {
				n, ok = float64(i), true
			}
		}
		if !ok || n < 1 || n != float64(int64(n)) || n > 1<<53 {
			return ArgErrorf(0, "@userSessions oauth.%s must be a positive whole number of seconds", key)
		}
		*dst = int64(n)
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		value := obj[key]
		var err error
		switch key {
		case "accessTokenTtlSeconds":
			err = seconds(key, value, &cfg.AccessTokenTTLSeconds)
		case "refreshTokenTtlSeconds":
			err = seconds(key, value, &cfg.RefreshTokenTTLSeconds)
		case "grantMaxTtlSeconds":
			err = seconds(key, value, &cfg.GrantMaxTTLSeconds)
		case "consentPath":
			path, ok := value.(string)
			if !ok || path == "" {
				err = ArgErrorf(0, "@userSessions oauth.consentPath must be a non-empty string, the path or URL of the project's consent page")
			}
			cfg.ConsentPath = path
		case "clients":
			cfg.Clients, err = identityRoutesOAuthClients(value)
		default:
			err = ArgErrorf(0, "@userSessions oauth has unknown key %q; it takes accessTokenTtlSeconds, clients, consentPath, grantMaxTtlSeconds and refreshTokenTtlSeconds", key)
		}
		if err != nil {
			return nil, err
		}
	}
	switch {
	case cfg.AccessTokenTTL() >= cfg.RefreshTokenTTL():
		return nil, ArgErrorf(0, "@userSessions oauth: an access token's lifetime (%d s) must be shorter than a refresh token's (%d s)", cfg.AccessTokenTTL(), cfg.RefreshTokenTTL())
	case cfg.RefreshTokenTTL() > cfg.GrantMaxTTL():
		return nil, ArgErrorf(0, "@userSessions oauth: a refresh token's lifetime (%d s) must be no longer than a grant's (%d s)", cfg.RefreshTokenTTL(), cfg.GrantMaxTTL())
	case !cfg.MetadataDocuments() && !cfg.Registration():
		return nil, ArgErrorf(0, "@userSessions oauth.clients turns off both kinds of client, so no client could connect; keep metadataDocuments or registration")
	}
	return cfg, nil
}

// identityRoutesOAuthClients reads oauth.clients: { metadataDocuments?,
// registration? }, each true or false.
func identityRoutesOAuthClients(v any) (*ir.IdentityOAuthClients, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@userSessions oauth.clients must be an object literal: { metadataDocuments?, registration? }")
	}
	clients := &ir.IdentityOAuthClients{}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		var dst **bool
		switch key {
		case "metadataDocuments":
			dst = &clients.MetadataDocuments
		case "registration":
			dst = &clients.Registration
		default:
			return nil, ArgErrorf(0, "@userSessions oauth.clients has unknown key %q; it takes metadataDocuments and registration", key)
		}
		flag, ok := obj[key].(bool)
		if !ok {
			return nil, ArgErrorf(0, "@userSessions oauth.clients.%s must be true or false", key)
		}
		*dst = &flag
	}
	return clients, nil
}

// applyUserAdministration reads @userAdministration({ path?, apiKeys?,
// oauth? }).
func applyUserAdministration(args []any, set *ir.OperationSet) error {
	const name = "userAdministration"
	if err := identityRoutesDeclared(name, set); err != nil {
		return err
	}
	cfg := &ir.UserAdministrationConfig{}
	err := identityRoutesConfig(name, args, map[string]func(any) error{
		"path":    func(v any) error { return identityRoutesPath(name, v, &cfg.Path) },
		"apiKeys": func(v any) error { return identityRoutesFlag(name, "apiKeys", v, &cfg.APIKeys) },
		"oauth":   func(v any) error { return identityRoutesFlag(name, "oauth", v, &cfg.OAuth) },
	})
	if err != nil {
		return err
	}
	set.UserAdministration = cfg
	return nil
}

// identityRoutesDeclared refuses a second route decorator on the class:
// the same one again, the other, @behaviorOperations (D103) or
// @graphOperations (D138).
func identityRoutesDeclared(name string, set *ir.OperationSet) error {
	switch {
	case name == "userSessions" && set.UserSessions != nil, name == "userAdministration" && set.UserAdministration != nil:
		return fmt.Errorf("@%s is declared twice on the same class", name)
	case set.UserSessions != nil:
		return fmt.Errorf("@%s contradicts @userSessions on the same class: a class takes one of them", name)
	case set.UserAdministration != nil:
		return fmt.Errorf("@%s contradicts @userAdministration on the same class: a class takes one of them", name)
	case set.BehaviorOperations != nil:
		return fmt.Errorf("@%s contradicts @behaviorOperations on the same class: a class takes one of them", name)
	case set.GraphOperations != nil:
		return fmt.Errorf("@%s contradicts @graphOperations on the same class: a class takes one of them", name)
	case set.RowFeatureOperations != nil:
		return fmt.Errorf("@%s contradicts @rowFeatureOperations on the same class: a class takes one of them", name)
	case set.TriageOperations != nil:
		return fmt.Errorf("@%s contradicts @triageOperations on the same class: a class takes one of them", name)
	case set.AccessGrantsOperations != nil:
		return fmt.Errorf("@%s contradicts @accessGrantsOperations on the same class: a class takes one of them", name)
	}
	return nil
}

// identityRoutesConfig reads the optional config object, calling the
// setter of each key in key order and refusing a key it has none for.
func identityRoutesConfig(name string, args []any, setters map[string]func(any) error) error {
	if len(args) > 1 {
		return fmt.Errorf("@%s takes at most one config object", name)
	}
	if len(args) == 0 || args[0] == nil {
		return nil
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return ArgErrorf(0, "@%s config must be an object literal", name)
	}
	keys := make([]string, 0, len(cfg))
	for key := range cfg {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		set, ok := setters[key]
		if !ok {
			known := make([]string, 0, len(setters))
			for k := range setters {
				known = append(known, k)
			}
			sort.Strings(known)
			return ArgErrorf(0, "@%s config has unknown key %q; it takes %s", name, key, joinKeys(known))
		}
		if err := set(cfg[key]); err != nil {
			return err
		}
	}
	return nil
}

func identityRoutesPath(name string, v any, dst *string) error {
	path, ok := v.(string)
	if !ok {
		return ArgErrorf(0, "@%s path must be a string literal", name)
	}
	*dst = path
	return nil
}

func identityRoutesFlag(name, key string, v any, dst *bool) error {
	flag, ok := v.(bool)
	if !ok {
		return ArgErrorf(0, "@%s %s must be true or false", name, key)
	}
	*dst = flag
	return nil
}

// joinKeys lists keys as "a", "a and b" or "a, b and c".
func joinKeys(keys []string) string {
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return keys[0]
	}
	out := keys[0]
	for _, key := range keys[1 : len(keys)-1] {
		out += ", " + key
	}
	return out + " and " + keys[len(keys)-1]
}
