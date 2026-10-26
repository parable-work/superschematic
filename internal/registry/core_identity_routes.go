package registry

import (
	"fmt"
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
// registration?, defaultRoles?, firstUser? }). login is true and register
// false when absent, and register needs the login; registration,
// defaultRoles and firstUser (D81) need register.
func applyUserSessions(args []any, set *ir.OperationSet) error {
	const name = "userSessions"
	if err := identityRoutesDeclared(name, set); err != nil {
		return err
	}
	cfg := &ir.UserSessionsConfig{}
	login, register := true, false
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
	cfg.NoLogin, cfg.Register = !login, register
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

// applyUserAdministration reads @userAdministration({ path? }).
func applyUserAdministration(args []any, set *ir.OperationSet) error {
	const name = "userAdministration"
	if err := identityRoutesDeclared(name, set); err != nil {
		return err
	}
	cfg := &ir.UserAdministrationConfig{}
	err := identityRoutesConfig(name, args, map[string]func(any) error{
		"path": func(v any) error { return identityRoutesPath(name, v, &cfg.Path) },
	})
	if err != nil {
		return err
	}
	set.UserAdministration = cfg
	return nil
}

// identityRoutesDeclared refuses a second route decorator on the class:
// the same one again, or the other.
func identityRoutesDeclared(name string, set *ir.OperationSet) error {
	switch {
	case name == "userSessions" && set.UserSessions != nil, name == "userAdministration" && set.UserAdministration != nil:
		return fmt.Errorf("@%s is declared twice on the same class", name)
	case set.UserSessions != nil:
		return fmt.Errorf("@%s contradicts @userSessions on the same class: a class takes one of them", name)
	case set.UserAdministration != nil:
		return fmt.Errorf("@%s contradicts @userAdministration on the same class: a class takes one of them", name)
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
