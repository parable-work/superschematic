package cloudflare

import (
	"slices"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// Calls between Workers (docs/stack-model.md, sections 18.2 and 18.10,
// D64). A Worker reaches another through a service binding, which admits
// it: a Worker that is not exposed has no route, so only a Worker holding a
// binding to it reaches it. The caller identifies itself with D37's signed
// token, signed with the generic connector's per-edge Ed25519 key (D63), a
// `tls` PrivateKey node the core registers, whose private key's PEM reaches
// the caller as a secret_text binding through the secret flag, and whose
// public key the callee's callers field holds. No runtime reads a service
// binding's ctx.props.
//
// The binding names the callee's Worker through the output of its worker
// node, which sits in infrastructure: the callee's Worker exists before a
// caller's version binds to it, and the graph knows the caller needs it.
// The waves (section 5.3) roll the callee's version out first. The caller's
// own worker node depends on the callee's too (bindingDependencies), so a
// destroy removes the caller, with its bindings, before the callee.

// WorkersHTTPConnector realizes an http edge from a Worker to a Worker.
const WorkersHTTPConnector = "cloudflare.workers-workers"

// connectWorkers derives the callee's service endpoint for the caller:
//
//   - url, the callee's address, `https://<callee's Worker>`, the host the
//     binding's requests carry;
//   - binding, a service binding named after the derived field to the
//     callee's Worker, which the platform writes (resourceBinding);
//   - credential, a signed token whose iss and sub are the caller's
//     deployable and whose aud is the callee's, signed with the edge key's
//     private key, a secret member.
//
// It returns the edge's key node, which two edges from one caller to two
// APIs of one callee share, and the callee's issuer: the caller, the key's
// public key, EdDSA, tokens that live at most 300 seconds. A call between
// two APIs one server serves stays in process: its value is the server's
// own address, with no binding and no credential, and fetch.ts hands the
// request to its own app.
func connectWorkers(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if from.Name == to.Name {
		return registry.Connected{Value: ir.ServiceEndpoint{URL: to.Address}}, nil
	}
	key := stack.GenericKeyPairID(from.Name, to.Name)
	serves := make([]string, len(from.Services))
	for i, ref := range from.Services {
		serves[i] = ref.Name
	}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:         key,
			Type:       stack.GenericKeyType,
			Properties: map[string]any{"algorithm": stack.GenericKeyAlgorithm},
			Phase:      ir.PhaseInfrastructure,
		}},
		Value: ir.ServiceEndpoint{
			URL: to.Address,
			Binding: &ir.ServiceBinding{
				Name:   ctx.Edge.Field,
				Worker: ir.Output{Resource: workerID(to.Name), Name: "name"},
			},
			Credential: &ir.ServiceCredential{
				Source:   ir.CredentialSignedToken,
				Audience: to.Name,
				Issuer:   from.Name,
				Key:      ir.Output{Resource: key, Name: stack.GenericKeyPrivateOutput},
			},
		},
		Callee: ir.ServiceAuthIssuer{
			Issuer:             from.Name,
			Audience:           to.Name,
			Algorithms:         []string{ir.AlgorithmEdDSA},
			Keys:               []ir.ServiceAuthKey{{PEM: ir.Output{Resource: key, Name: stack.GenericKeyPublicOutput}}},
			MaxLifetimeSeconds: stack.GenericSignedTokenLifetime,
			Callers:            []ir.ServiceAuthCaller{{Subject: from.Name, Deployable: from.Name, Serves: serves}},
		},
	}, nil
}

// bindingDependencies returns the worker nodes of the Workers a
// deployable's service bindings name, sorted, which its own worker node
// depends on: Cloudflare refuses to delete a Worker another Worker's
// binding names, so a destroy must remove the caller first.
func bindingDependencies(d ir.ResolvedDeployable) []string {
	var out []string
	for _, b := range d.Bindings {
		if b.Source != ir.BindingDerived {
			continue
		}
		var worker any
		switch v := b.Value.(type) {
		case ir.ServiceEndpoint:
			if v.Binding != nil {
				worker = v.Binding.Worker
			}
		case map[string]any:
			if binding, ok := v["binding"].(map[string]any); ok {
				worker = binding["worker"]
			}
		}
		outputs, _ := ir.ValueRefs(worker)
		for _, o := range outputs {
			out = append(out, o.Resource)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
