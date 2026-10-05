package ir

import (
	"encoding/json"
	"fmt"
	"sort"
)

// The resource graph is the contract between platforms and connectors on
// one side and provisioners on the other (docs/stack-model.md, section
// 6.4). Resource types and properties use Pulumi package schemas as their
// vocabulary (`gcp:cloudrunv2/service:Service`).
//
// A property, a binding's derived value, a deployable's name or address
// and a DNS record may hold a reference instead of a plain JSON value:
//
//	{"$output": {"resource": "shop-api.service", "name": "uri"}}  another node's output
//	{"$parameter": "pr"}                                            a parameter of the environment
//	{"$concat": ["shop-api-", {"$parameter": "pr"}]}                strings and references joined
//
// An object whose one key is `$output`, `$parameter` or `$concat` is
// always read as a reference, so a literal property cannot be such an
// object. In Go the references are Output, Parameter and Concat values;
// DecodeValue turns their JSON form back into them.

// ResourceGraph is the resources an environment lowers to.
type ResourceGraph struct {
	// Parameters are the environment's parameters, which properties may
	// reference.
	Parameters []string `json:"parameters,omitempty"`

	// Resources are the nodes, sorted by ID.
	Resources []*Resource `json:"resources"`
}

// Resource returns the node with id, or nil.
func (g *ResourceGraph) Resource(id string) *Resource {
	if g == nil {
		return nil
	}
	for _, r := range g.Resources {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// ResourcePhase is the deploy step a resource belongs to before its
// dependencies are considered (section 5.3).
type ResourcePhase string

const (
	// PhaseInfrastructure is applied before any migration or server.
	PhaseInfrastructure ResourcePhase = "infrastructure"

	// PhaseRollout is applied with the servers, callees before callers.
	PhaseRollout ResourcePhase = "rollout"

	// PhaseExposure is applied last.
	PhaseExposure ResourcePhase = "exposure"
)

// Rank orders the phases: infrastructure, rollout, exposure. An unknown
// phase ranks -1.
func (p ResourcePhase) Rank() int {
	switch p {
	case PhaseInfrastructure:
		return 0
	case PhaseRollout:
		return 1
	case PhaseExposure:
		return 2
	}
	return -1
}

// Resource is one node of the graph.
type Resource struct {
	// ID is unique in the graph. Platforms and connectors choose it, and
	// it is stable across runs, because provisioners key state on it.
	ID string `json:"id"`

	// Type is the resource type token (`gcp:cloudrunv2/service:Service`).
	Type string `json:"type"`

	// Properties are the resource's inputs. Values may hold references.
	Properties map[string]any `json:"properties,omitempty"`

	// DependsOn lists the nodes this one needs first: the ones its
	// producer named, plus every node its properties reference. Sorted.
	DependsOn []string `json:"dependsOn,omitempty"`

	// Phase is the deploy step the node belongs to. A producer may leave
	// it empty for its default: rollout for a server's own resources,
	// exposure for DNS records, infrastructure otherwise. A node whose
	// dependency sits in a later phase moves there, and resolution writes
	// the phase it landed in; the deploy order names its rollout wave.
	Phase ResourcePhase `json:"phase,omitempty"`

	// Inherited marks a node of the parent environment that a member of a
	// parameterized environment reads and does not create, such as the
	// database instance the members share (section 5.4).
	Inherited bool `json:"inherited,omitempty"`

	// Owners name what produced the node: a deployable's name, an edge's
	// ID, or "dns". Two producers that return the same node share it.
	// Resolution sets it. Sorted.
	Owners []string `json:"owners,omitempty"`
}

// Output references an output of another resource.
type Output struct {
	// Resource is the referenced node's ID.
	Resource string `json:"resource"`

	// Name is the output's name.
	Name string `json:"name"`
}

// MarshalJSON writes {"$output": {"resource", "name"}}.
func (o Output) MarshalJSON() ([]byte, error) {
	type plain Output
	return json.Marshal(map[string]plain{"$output": plain(o)})
}

// Parameter references one of the environment's parameters, whose value
// the deploy run supplies.
type Parameter string

// MarshalJSON writes {"$parameter": name}.
func (p Parameter) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"$parameter": string(p)})
}

// Concat joins strings and references into one string, such as a name
// suffixed with a parameter's value.
type Concat []any

// MarshalJSON writes {"$concat": [parts...]}.
func (c Concat) MarshalJSON() ([]byte, error) {
	parts := []any(c)
	if parts == nil {
		parts = []any{}
	}
	return json.Marshal(map[string][]any{"$concat": parts})
}

// DecodeValue turns the JSON form of a value, as encoding/json decodes it
// into any, back into one that holds Output, Parameter and Concat values.
// It returns an error for a malformed reference.
func DecodeValue(v any) (any, error) {
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 1 {
			for key, inner := range v {
				switch key {
				case "$output":
					return decodeOutput(inner)
				case "$parameter":
					name, ok := inner.(string)
					if !ok || name == "" {
						return nil, fmt.Errorf("$parameter must be a parameter name")
					}
					return Parameter(name), nil
				case "$concat":
					return decodeConcat(inner)
				}
			}
		}
		out := make(map[string]any, len(v))
		for key, inner := range v {
			decoded, err := DecodeValue(inner)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = decoded
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			decoded, err := DecodeValue(inner)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = decoded
		}
		return out, nil
	}
	return v, nil
}

func decodeOutput(v any) (Output, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 2 {
		return Output{}, fmt.Errorf("$output must be an object with resource and name")
	}
	resource, rok := m["resource"].(string)
	name, nok := m["name"].(string)
	if !rok || !nok || resource == "" || name == "" {
		return Output{}, fmt.Errorf("$output must be an object with resource and name")
	}
	return Output{Resource: resource, Name: name}, nil
}

func decodeConcat(v any) (Concat, error) {
	parts, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("$concat must be a list")
	}
	out := make(Concat, len(parts))
	for i, part := range parts {
		decoded, err := DecodeValue(part)
		if err != nil {
			return nil, fmt.Errorf("$concat[%d]: %w", i, err)
		}
		switch decoded.(type) {
		case string, Output, Parameter:
		default:
			return nil, fmt.Errorf("$concat[%d] must be a string, an $output or a $parameter", i)
		}
		out[i] = decoded
	}
	return out, nil
}

// ValueRefs returns the outputs and the parameters a value references,
// each in the order it first appears.
func ValueRefs(v any) (outputs []Output, parameters []string) {
	seenOut := map[Output]bool{}
	seenParam := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case Output:
			if !seenOut[v] {
				seenOut[v] = true
				outputs = append(outputs, v)
			}
		case Parameter:
			if !seenParam[string(v)] {
				seenParam[string(v)] = true
				parameters = append(parameters, string(v))
			}
		case Concat:
			for _, part := range v {
				walk(part)
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key])
			}
		case []any:
			for _, inner := range v {
				walk(inner)
			}
		}
	}
	walk(v)
	return outputs, parameters
}

// UnmarshalJSON decodes a resource and turns the references in its
// properties back into Output, Parameter and Concat values.
func (r *Resource) UnmarshalJSON(data []byte) error {
	type plain Resource
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Properties != nil {
		decoded, err := DecodeValue(map[string]any(p.Properties))
		if err != nil {
			return fmt.Errorf("resource %s: properties: %w", p.ID, err)
		}
		p.Properties = decoded.(map[string]any)
	}
	*r = Resource(p)
	return nil
}
