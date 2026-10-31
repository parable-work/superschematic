package toolsutil

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
)

// BehaviorMember is a namespace's member holding the operations of one
// behavior use (D103): Use is its id, Type its table, Endpoints its
// operations in order.
type BehaviorMember struct {
	Use, Type, Behavior string
	Endpoints           []apigen.EndpointInfo
}

// reservedBehaviorMembers are the members every SDK's namespace holds
// itself, which a use may not take.
var reservedBehaviorMembers = []string{"client", "constructor"}

// BehaviorMembers groups the endpoints of namespace by use, in first-seen
// order, and refuses a member that would collide with an operation of the
// namespace or a member the SDKs reserve (client, constructor), naming
// both. Two names collide when any SDK's spelling of them is one (status,
// Status, STATUS): their words, joined and lowercased, are equal. An
// endpoint whose BehaviorOperation is nil is an operation of the namespace,
// which keeps its flat method.
func BehaviorMembers(namespace string, endpoints []apigen.EndpointInfo) ([]BehaviorMember, error) {
	var members []BehaviorMember
	index := map[string]int{}
	for _, ep := range endpoints {
		marker := ep.BehaviorOperation()
		if marker == nil {
			continue
		}
		key := marker.Type + "." + marker.Use
		if i, ok := index[key]; ok {
			members[i].Endpoints = append(members[i].Endpoints, ep)
			continue
		}
		index[key] = len(members)
		members = append(members, BehaviorMember{Use: marker.Use, Type: marker.Type, Behavior: marker.Behavior, Endpoints: []apigen.EndpointInfo{ep}})
	}
	for i, member := range members {
		folded := foldName(member.Use)
		for _, reserved := range reservedBehaviorMembers {
			if folded == foldName(reserved) {
				return nil, fmt.Errorf("toolsutil: namespace %s holds the operations of %s's %s, which every SDK puts under %s.%s, which the SDKs reserve; rename the field", namespace, member.Type, member.Use, namespace, member.Use)
			}
		}
		for _, ep := range endpoints {
			if ep.BehaviorOperation() == nil && foldName(ep.Name) == folded {
				return nil, fmt.Errorf("toolsutil: namespace %s holds the operation %s and the operations of %s's %s, which every SDK puts under %s.%s; rename the operation, or give the set a name of another namespace", namespace, ep.Name, member.Type, member.Use, namespace, member.Use)
			}
		}
		for _, other := range members[:i] {
			if foldName(other.Use) == folded {
				return nil, fmt.Errorf("toolsutil: namespace %s holds the operations of %s's %s and of %s's %s, which every SDK puts under %s.%s; give one set a name of another namespace", namespace, other.Type, other.Use, member.Type, member.Use, namespace, member.Use)
			}
		}
	}
	return members, nil
}

// foldName is a name's words joined and lowercased, the form every SDK's
// spelling of it shares.
func foldName(name string) string {
	return strings.ReplaceAll(codegen.ToSnakeCase(name), "_", "")
}

// BehaviorMethodName is the method path of a behavior operation below its
// namespace, as the TypeScript SDK names it: its use's member and its
// operation's method (status.transition, paymentStatus.transition).
func BehaviorMethodName(use, operation string) string {
	return tsutil.ToCamelCase(use) + "." + tsutil.ToCamelCase(operation)
}

// BehaviorToolName is the name the SDK tool documents give a behavior
// operation's tool (D103): its namespace and its method path, the
// TypeScript SDK's (order.status.transition), as apigen.ToolName names
// any other operation's.
func BehaviorToolName(namespace, use, operation string) string {
	if strings.TrimSpace(namespace) == "" {
		namespace = "root"
	}
	return namespace + "." + BehaviorMethodName(use, operation)
}

// EndpointToolName is the tool name of ep: BehaviorToolName for a behavior
// operation, apigen.ToolName for any other.
func EndpointToolName(ep apigen.EndpointInfo) string {
	if marker := ep.BehaviorOperation(); marker != nil {
		return BehaviorToolName(ep.Namespace, marker.Use, marker.Operation)
	}
	return apigen.ToolName(ep.Namespace, ep.Name)
}

// EndpointMethodName is ep's method name below its namespace, as the
// TypeScript SDK names it: BehaviorMethodName for a behavior operation,
// the operation's camelCase name for any other.
func EndpointMethodName(ep apigen.EndpointInfo) string {
	if marker := ep.BehaviorOperation(); marker != nil {
		return BehaviorMethodName(marker.Use, marker.Operation)
	}
	return tsutil.ToCamelCase(ep.Name)
}

// BehaviorMemberPhrase describes a behavior use's member, for a doc
// comment: "the operations of Order's status, a Workflow (D103)".
func BehaviorMemberPhrase(typeName, use, behavior string) string {
	article := "a "
	if behavior != "" && strings.ContainsRune("AEIOUaeiou", rune(behavior[0])) {
		article = "an "
	}
	return "the operations of " + typeName + "'s " + use + ", " + article + behavior + " (D103)"
}

// BehaviorMemberDoc is BehaviorMemberPhrase as a sentence: "The operations
// of Order's status, a Workflow (D103)."
func BehaviorMemberDoc(typeName, use, behavior string) string {
	phrase := BehaviorMemberPhrase(typeName, use, behavior)
	return strings.ToUpper(phrase[:1]) + phrase[1:] + "."
}
