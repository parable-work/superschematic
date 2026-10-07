package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// terminal answers each prompt with the next of its answers.
type terminal struct {
	answers []string
	prompts []string
}

func (t *terminal) Secret(prompt string) ([]byte, error) {
	t.prompts = append(t.prompts, prompt)
	if len(t.answers) == 0 {
		return nil, fmt.Errorf("the terminal has no more answers")
	}
	answer := t.answers[0]
	t.answers = t.answers[1:]
	return []byte(answer), nil
}

// TestBootstrapAndDeployReadTheToken deploys the shop's Staging, its
// records on Cloudflare, on the fake target: bootstrap hands the target
// the zone's token from dns.credentials, so it creates the token's secret,
// and asks for its value; a deploy reads the value back and hands it to
// the provisioner in CLOUDFLARE_API_TOKEN, and never logs it.
func TestBootstrapAndDeployReadTheToken(t *testing.T) {
	ext := &stacktest.Extension{}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, cloudflare.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	env := resolve(t, reg, shop(), "Staging")
	secret := cloudflare.TokenSecret("shop-stack", "acme.dev")
	ctx := context.Background()
	log := &bytes.Buffer{}
	options := stack.Options{Registry: reg, Run: registry.Run{Environment: env}, Dir: t.TempDir(), Log: log}

	term := &terminal{answers: []string{"cf-token-value"}}
	if _, err := stack.Bootstrap(ctx, stack.BootstrapOptions{Options: options, Repository: "acme/shop", Prompter: term}); err != nil {
		t.Fatal(err)
	}
	if got, want := ext.Provisioner.Calls(), []string{"bootstrap Staging: repository acme/shop, credentials " + secret}; !slices.Equal(got, want) {
		t.Errorf("bootstrap ran %q, want %q", got, want)
	}
	if len(term.prompts) != 1 || !strings.Contains(term.prompts[0], "DNS Edit permission on zone acme.dev") {
		t.Errorf("bootstrap asked %q", term.prompts)
	}
	if got, err := ext.Secrets.Get(ctx, env, secret); err != nil || string(got) != "cf-token-value" {
		t.Fatalf("the token's secret holds %q, %v", got, err)
	}

	if err := ext.Secrets.Set(ctx, env, "PaymentsSecrets.STRIPE_KEY", []byte("sk_test_value")); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	_, err = stack.Deploy(ctx, stack.DeployOptions{
		Options: options,
		Images:  map[string]string{"shop-api": "shop-api@" + digest, "Orders": "orders@" + digest},
		Planner: func(service, dialect string, _ json.RawMessage) (*stack.DatabasePlan, error) {
			return &stack.DatabasePlan{
				Service: service, Dialect: dialect,
				To: "v1", ToModel: json.RawMessage(`{"v":1}`),
				ExpandSteps: 1, Hash: "plan", Document: json.RawMessage(`{"plan":1}`),
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ext.Provisioner.Env()["CLOUDFLARE_API_TOKEN"]; got != "cf-token-value" {
		t.Errorf("the provisioner was handed CLOUDFLARE_API_TOKEN %q", got)
	}
	if strings.Contains(log.String(), "cf-token-value") {
		t.Errorf("the log holds the token:\n%s", log)
	}
}
