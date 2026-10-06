package gcp

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// serverlessAuthorizationHeader is the header Cloud Run's invoker check
// reads an ID token from when it is present, which leaves the end user's
// Authorization to the application (section 9.2).
const serverlessAuthorizationHeader = "X-Serverless-Authorization"

// connectSQL realizes a sql edge from a Cloud Run server to a Cloud SQL
// database (sections 7.2 and 7.4). The server's account gets the Cloud
// SQL client role, to reach the instance through the connector, and the
// instance user role, to log in with IAM, both held to the edge's
// instance by an IAM condition; and an IAM database user on the instance.
// The derived value is the Cloud SQL connection the server's connector
// dials, so there is no password.
//
// A Rust server has no Cloud SQL connector to dial with. Section 7.4's
// other form, a generated password and Cloud Run's Cloud SQL mount, waits
// for a password form of the derived value, so the edge is refused.
func connectSQL(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if from.Language == registry.APILanguageRust {
		return registry.Connected{}, fmt.Errorf("server %s is a %s server, which has no Cloud SQL connector to reach %s with IAM authentication; the password form of docs/stack-model.md section 7.4 is not built", from.Name, from.Language, to.Name)
	}
	v := valuesOf(ctx.Environment)
	account := from.Name + ".account"
	member := ir.Output{Resource: account, Name: "member"}
	condition := map[string]any{
		"title": join("Cloud SQL instance ", to.ResourceName),
		"expression": join(
			`resource.type == "sqladmin.googleapis.com/Instance" && resource.name == "projects/`,
			v.project, "/instances/", to.ResourceName, `"`,
		),
	}
	grant := func(part, role string) *ir.Resource {
		return &ir.Resource{ID: from.Name + "." + part + "." + to.Name, Type: TypeProjectIAMMember, Properties: map[string]any{
			"project":   v.project,
			"role":      role,
			"member":    member,
			"condition": condition,
		}}
	}
	// A service account's Postgres user is its email without
	// `.gserviceaccount.com`. The account's id is the server's name.
	user := join(from.ResourceName, "@", v.project, ".iam")
	return registry.Connected{
		Resources: []*ir.Resource{
			grant("cloudsql-client", "roles/cloudsql.client"),
			grant("cloudsql-login", "roles/cloudsql.instanceUser"),
			{
				ID:        from.Name + ".database-user." + to.Name,
				Type:      TypeUser,
				DependsOn: []string{account},
				Properties: map[string]any{
					"project":  v.project,
					"instance": ir.Output{Resource: to.Name + ".instance", Name: "name"},
					"name":     user,
					"type":     "CLOUD_IAM_SERVICE_ACCOUNT",
				},
			},
		},
		Value: ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
			Instance: to.Address,
			Database: ir.Output{Resource: to.Name + ".database." + ctx.Edge.Service.Name, Name: "name"},
			User:     user,
		}},
	}, nil
}

// connectHTTP realizes an http edge from a Cloud Run server to one it
// calls (sections 7.2 and 9.2). The caller's account gets the invoker role
// on the callee, which Cloud Run's invoker check admits an internal callee
// by. The derived value is the callee's run.app URL, which the caller
// reaches through the VPC (lowerService), and a Google ID token for it from
// the metadata server as the service credential. The token travels in
// Service-Authorization, which the callee verifies, and to an internal
// callee also in X-Serverless-Authorization, which the invoker check reads
// so the end user's Authorization reaches the application.
//
// A server that calls an API it serves itself reaches it over loopback,
// needs no grant and sends no credential.
func connectHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	if from.Name == to.Name {
		return registry.Connected{Value: ir.ServiceEndpoint{URL: fmt.Sprintf("http://127.0.0.1:%d", containerPort)}}, nil
	}
	v := valuesOf(ctx.Environment)
	credential := &ir.ServiceCredential{Source: ir.CredentialGoogleIDToken, Audience: to.Address}
	if !to.Exposed {
		credential.Headers = []string{ir.ServiceAuthorizationHeader, serverlessAuthorizationHeader}
	}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   from.Name + ".run-invoker." + to.Name,
			Type: TypeServiceIAMMember,
			Properties: map[string]any{
				"project":  v.project,
				"location": v.region,
				"name":     ir.Output{Resource: to.Name + ".service", Name: "name"},
				"role":     "roles/run.invoker",
				"member":   ir.Output{Resource: from.Name + ".account", Name: "member"},
			},
		}},
		Value: ir.ServiceEndpoint{URL: to.Address, Credential: credential},
	}, nil
}
