// Command use is code outside a stack, built against the golden bindings:
// a script reads the values of applied environments, and a hand-written
// Pulumi program reads environments over stack references. The compile
// test vets it and runs the script half.
package main

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/parable-work/superschematic/extensions/pulumi/bindings/testdata/golden/kinds"
	"github.com/parable-work/superschematic/extensions/pulumi/bindings/testdata/golden/shopstack"
	shopstackpulumi "github.com/parable-work/superschematic/extensions/pulumi/bindings/testdata/golden/shopstack/pulumi"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pulumi" {
		pulumi.Run(program)
		return
	}
	api := shopstack.Staging.ShopAPI
	fmt.Println(api.Name, api.Address, api.Account.Email)
	for _, name := range []string{"Production", "Staging"} {
		fmt.Println(name, shopstack.Environments[name].ShopDB.Instance.ConnectionName)
	}
	limits := kinds.One.Worker.Limits
	labels, _ := limits.Labels.(map[string]any)
	fmt.Println(kinds.One.Worker.Address, limits.Enabled, limits.CPU, labels["tier"])
}

// program grants a hand-written resource's account on the staging API, and
// exports a preview member's address.
func program(ctx *pulumi.Context) error {
	staging, err := shopstackpulumi.Staging(ctx)
	if err != nil {
		return err
	}
	preview, err := shopstackpulumi.Preview(ctx, "123")
	if err != nil {
		return err
	}
	ctx.Export("stagingAccount", staging.ShopAPI.Account.Email)
	ctx.Export("previewAddress", preview.ShopAPI.Address)
	ctx.Export("project", pulumi.String(shopstackpulumi.Project))
	return nil
}
