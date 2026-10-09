package registry

import (
	"fmt"
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

// applyUserSessions reads @userSessions({ path?, login?, register? }).
// login is true and register false when absent, and register needs the
// login.
func applyUserSessions(args []any, set *ir.OperationSet) error {
	const name = "userSessions"
	if err := identityRoutesDeclared(name, set); err != nil {
		return err
	}
	cfg := &ir.UserSessionsConfig{}
	login, register := true, false
	err := identityRoutesConfig(name, args, map[string]func(any) error{
		"path": func(v any) error { return identityRoutesPath(name, v, &cfg.Path) },
		"login": func(v any) error {
			return identityRoutesFlag(name, "login", v, &login)
		},
		"register": func(v any) error {
			return identityRoutesFlag(name, "register", v, &register)
		},
	})
	if err != nil {
		return err
	}
	if register && !login {
		return ArgErrorf(0, "@userSessions: register needs the login; drop register: true or login: false")
	}
	cfg.NoLogin, cfg.Register = !login, register
	set.UserSessions = cfg
	return nil
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
