package behaviorops_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

func testCommentsAndWorkflowSchemas(mcp *ir.BehaviorOperationsMCP, serviceOnly bool) (api *ir.Schema, db *ir.Schema, reg *registry.Registry) {
	reg = registry.New(naming.Naming{})
	db = ir.NewSchema("household-db", ir.SchemaKindDB)
	db.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", TypeMappings: map[string]string{"go": "string", "json_schema": "string"}}
	db.Types["Obligation"] = &ir.TypeDef{
		Name: "Obligation",
		Role: ir.RoleDBTable,
		Behaviors: []ir.BehaviorRef{
			{
				Name:       "Comments",
				Implements: true,
				Config:     []byte(`{}`),
			},
			{
				Name:   "Workflow",
				Field:  "status",
				Config: []byte(`{"states":["draft","active","closed"],"initial":"draft","transitions":[{"from":"draft","to":"active"},{"from":"active","to":"closed"}]}`),
			},
		},
		Fields: []*ir.FieldDef{
			{Name: "id", Key: true, Required: true, TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
			{Name: "status", Required: true, TypeRef: ir.TypeRef{Name: "string"}},
		},
	}

	api = ir.NewSchema("household-api", ir.SchemaKindAPI)
	api.Dependencies = []ir.ServiceRef{{Name: "household-db", Kind: ir.SchemaKindDB}}
	api.Imports = []ir.Import{{Package: "@schemas/household-db", Types: []string{"Obligation"}}}
	set := &ir.OperationSet{
		Name:               "ObligationActions",
		Auth:               true,
		BehaviorOperations: &ir.BehaviorOperationsConfig{Type: "Obligation", MCP: mcp},
	}
	if serviceOnly {
		set.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	}
	api.OperationSets = []*ir.OperationSet{set}
	return api, db, reg
}

func TestBehaviorOperationsMCPControls(t *testing.T) {
	mcp := &ir.BehaviorOperationsMCP{
		WriteInvocationPolicy: "ask",
		Behaviors: map[string]*ir.BehaviorOperationMCP{
			"comments": {Hidden: true, Reason: "Keep comment surface small for agents."},
		},
		Operations: map[string]*ir.BehaviorOperationMCP{
			"comments.listMessages": {},
			"comments.post":         {InvocationPolicy: "auto"},
		},
	}
	api, db, reg := testCommentsAndWorkflowSchemas(mcp, false)

	res := verify.CheckBehaviorOperations(api, verify.Input{Database: db, Registry: reg})
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected verify errors: %v", res.Errors)
	}
	plan, err := behaviorops.PlanSet(api.OperationSets[0], db, reg)
	if err != nil {
		t.Fatalf("PlanSet: %v", err)
	}

	byName := map[string]*ir.FieldDef{}
	for _, op := range plan.Operations {
		byName[op.Name] = op
	}

	// status.transition is a write -> inherits set writeInvocationPolicy: "ask".
	if op := byName["statusTransition"]; op == nil || op.MCP.Hidden || op.MCP.Invocation.Value != "ask" {
		t.Errorf("statusTransition MCP = %+v, want visible ask", op.MCP)
	}
	// comments.listMessages was explicitly unhidden and is a read -> visible, "auto".
	if op := byName["commentsListMessages"]; op == nil || op.MCP.Hidden || op.MCP.Invocation.Value != "auto" {
		t.Errorf("commentsListMessages MCP = %+v, want visible auto", op.MCP)
	}
	// comments.post was explicitly unhidden with invocationPolicy: "auto" -> overrides set writeInvocationPolicy: "ask".
	if op := byName["commentsPost"]; op == nil || op.MCP.Hidden || op.MCP.Invocation.Value != "auto" {
		t.Errorf("commentsPost MCP = %+v, want visible auto", op.MCP)
	}
	// Other comments operations remain hidden with the behavior's reason.
	if op := byName["commentsDeleteMessage"]; op == nil || !op.MCP.Hidden || op.MCP.HiddenReason != "Keep comment surface small for agents." {
		t.Errorf("commentsDeleteMessage MCP = %+v, want hidden with reason", op.MCP)
	}

	// Unhiding a write operation without its own policy still inherits set writeInvocationPolicy.
	mcp2 := &ir.BehaviorOperationsMCP{
		WriteInvocationPolicy: "ask",
		Behaviors: map[string]*ir.BehaviorOperationMCP{
			"comments": {Hidden: true, Reason: "Hide most comments."},
		},
		Operations: map[string]*ir.BehaviorOperationMCP{
			"commentsPost": {},
		},
	}
	api2, db2, reg2 := testCommentsAndWorkflowSchemas(mcp2, false)
	if res := verify.CheckBehaviorOperations(api2, verify.Input{Database: db2, Registry: reg2}); len(res.Errors) > 0 {
		t.Fatalf("unexpected verify errors (expanded key): %v", res.Errors)
	}
	plan2, err := behaviorops.PlanSet(api2.OperationSets[0], db2, reg2)
	if err != nil {
		t.Fatalf("PlanSet 2: %v", err)
	}
	for _, op := range plan2.Operations {
		if op.Name == "commentsPost" {
			if op.MCP.Hidden || op.MCP.Invocation.Value != "ask" {
				t.Errorf("commentsPost with inherited writeInvocationPolicy = %+v, want visible ask", op.MCP)
			}
		}
	}

	// Set-level hidden hides everything except explicitly exposed behavior/operation.
	mcp3 := &ir.BehaviorOperationsMCP{
		Hidden: true,
		Reason: "Not for agents by default.",
		Behaviors: map[string]*ir.BehaviorOperationMCP{
			"status": {InvocationPolicy: "ask"},
		},
	}
	api3, db3, reg3 := testCommentsAndWorkflowSchemas(mcp3, false)
	plan3, err := behaviorops.PlanSet(api3.OperationSets[0], db3, reg3)
	if err != nil {
		t.Fatalf("PlanSet 3: %v", err)
	}
	for _, op := range plan3.Operations {
		if op.Name == "statusTransition" {
			if op.MCP.Hidden || op.MCP.Invocation.Value != "ask" {
				t.Errorf("statusTransition = %+v, want visible ask", op.MCP)
			}
		} else if !op.MCP.Hidden || op.MCP.HiddenReason != "Not for agents by default." {
			t.Errorf("%s = %+v, want hidden with set reason", op.Name, op.MCP)
		}
	}

	// @requireService always wins and keeps operations hidden.
	api4, db4, reg4 := testCommentsAndWorkflowSchemas(&ir.BehaviorOperationsMCP{
		Operations: map[string]*ir.BehaviorOperationMCP{"status.transition": {InvocationPolicy: "auto"}},
	}, true)
	plan4, err := behaviorops.PlanSet(api4.OperationSets[0], db4, reg4)
	if err != nil {
		t.Fatalf("PlanSet 4: %v", err)
	}
	for _, op := range plan4.Operations {
		if !op.MCP.Hidden || !strings.Contains(op.MCP.HiddenReason, "@requireService") {
			t.Errorf("@requireService op %s MCP = %+v, want hidden by @requireService", op.Name, op.MCP)
		}
	}
}

func TestBehaviorOperationsMCPVerifyErrors(t *testing.T) {
	cases := []struct {
		name string
		mcp  *ir.BehaviorOperationsMCP
		want string
	}{
		{
			name: "unknown behavior",
			mcp: &ir.BehaviorOperationsMCP{
				Behaviors: map[string]*ir.BehaviorOperationMCP{
					"recurrence": {Hidden: true, Reason: "no recurrence"},
				},
			},
			want: `mcp.behaviors names "recurrence", which is not a behavior of Obligation`,
		},
		{
			name: "unknown operation",
			mcp: &ir.BehaviorOperationsMCP{
				Operations: map[string]*ir.BehaviorOperationMCP{
					"comments.bogus": {Hidden: true, Reason: "nope"},
				},
			},
			want: `mcp.operations names "comments.bogus", which is not a served operation of Obligation`,
		},
		{
			name: "aliased operation",
			mcp: &ir.BehaviorOperationsMCP{
				Operations: map[string]*ir.BehaviorOperationMCP{
					"status.transition": {InvocationPolicy: "ask"},
					"statusTransition":  {InvocationPolicy: "auto"},
				},
			},
			want: `which both refer to status.transition`,
		},
		{
			name: "hidden without reason in IR",
			mcp:  &ir.BehaviorOperationsMCP{Hidden: true},
			want: `hidden: true requires a non-empty reason`,
		},
		{
			name: "invalid policy in IR",
			mcp:  &ir.BehaviorOperationsMCP{WriteInvocationPolicy: "bogus"},
			want: `invocationPolicy "bogus" is not one of "auto", "ask"`,
		},
		{
			name: "writeInvocationPolicy on operation in IR",
			mcp: &ir.BehaviorOperationsMCP{
				Operations: map[string]*ir.BehaviorOperationMCP{
					"status.transition": {WriteInvocationPolicy: "ask"},
				},
			},
			want: `does not take writeInvocationPolicy`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, db, reg := testCommentsAndWorkflowSchemas(tc.mcp, false)
			res := verify.CheckBehaviorOperations(api, verify.Input{Database: db, Registry: reg})
			if len(res.Errors) == 0 {
				t.Fatalf("expected verify error holding %q, got none", tc.want)
			}
			var msgs []string
			for _, e := range res.Errors {
				msgs = append(msgs, e.Msg)
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("verify errors:\n%s\nwant %q", joined, tc.want)
			}
		})
	}
}
