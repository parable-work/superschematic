package pulumi

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/extensions/pulumi/internal/names"
)

// ProjectName returns the Pulumi project that holds a superschematic
// stack's environments: the stack's name in kebab case (`Shop` is `shop`).
// Each run of an environment is a Pulumi stack of the project (StackName).
func ProjectName(stack string) (string, error) {
	name := names.Kebab(stack)
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("stack name %q makes no Pulumi project name", stack)
	}
	return name, nil
}

// StackName returns the Pulumi stack that holds one run of an environment:
// the environment's name in kebab case, then each parameter's name and
// value in the environment's order. Staging is `staging`; Preview with
// parameter pr at 123 is `preview.pr-123`. A value is letters, digits,
// hyphens, underscores and dots, the characters a stack name allows.
func StackName(environment string, parameters []string, values map[string]string) (string, error) {
	name := names.Kebab(environment)
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("environment name %q makes no Pulumi stack name", environment)
	}
	if len(values) != len(parameters) {
		return "", fmt.Errorf("environment %s takes parameters %v, not %d values", environment, parameters, len(values))
	}
	var b strings.Builder
	b.WriteString(name)
	for _, param := range parameters {
		value, ok := values[param]
		if !ok {
			return "", fmt.Errorf("environment %s needs a value for parameter %s", environment, param)
		}
		if !valuePattern.MatchString(value) {
			return "", fmt.Errorf("parameter %s of environment %s is %q; a value is letters, digits, hyphens, underscores and dots", param, environment, value)
		}
		fmt.Fprintf(&b, ".%s-%s", names.Kebab(param), value)
	}
	return b.String(), nil
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	valuePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)
