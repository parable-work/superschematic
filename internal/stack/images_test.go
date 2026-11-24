package stack_test

import (
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// What a stack adds to its deployables' images (D144,
// docs/stack-model.md, section 8.2): `@stack` lists, per deployable that
// runs code, the Debian packages its image installs and the Dockerfile
// that extends it. The stack names them once, and every environment's
// resolution records them on the deployable.

// imageShop is the acceptance stack with images for shop-api's server, the
// declared server Orders, shop-orders' job and its worker.
func imageShop() (*ir.Stack, []stack.Service) {
	s := stacktest.Shop()
	s.Images = []*ir.DeployableImage{
		{Of: stacktest.Of(stacktest.ShopAPI), Packages: []string{"fonts-liberation", "chromium"}},
		{Of: ir.DeployableRef{Deployable: "Orders"}, Extend: "docker/orders.Dockerfile"},
		{Of: stacktest.JobOf(stacktest.ShopOrders, "ShipOrders"), Packages: []string{"poppler-utils"}, Extend: "docker/ship/Dockerfile"},
		{Of: stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders"), Packages: []string{"tini"}},
	}
	return s, stacktest.AcmeShop()
}

// TestImagesOfTheStack: each deployable the stack's images name carries
// them in every environment alike, its packages sorted, and a deployable
// they do not name carries none. Images reads the same from the stack
// alone, as the stack's build does for the Dockerfiles it writes.
func TestImagesOfTheStack(t *testing.T) {
	s, services := imageShop()
	want := map[string]*ir.ResolvedImage{
		"shop-api":                   {Packages: []string{"chromium", "fonts-liberation"}},
		"Orders":                     {Extend: "docker/orders.Dockerfile"},
		stacktest.ShipOrdersJob:      {Packages: []string{"poppler-utils"}, Extend: "docker/ship/Dockerfile"},
		stacktest.FulfilOrdersWorker: {Packages: []string{"tini"}},
	}
	images, err := stack.Images(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(images, want) {
		t.Errorf("Images = %+v, want %+v", images, want)
	}
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production"} {
		env := mustResolve(t, reg, s, services, name)
		for _, d := range env.Deployables {
			if !reflect.DeepEqual(d.Image, want[d.Name]) {
				t.Errorf("%s: %s %s carries image %+v, want %+v", name, d.Kind, d.Name, d.Image, want[d.Name])
			}
		}
		// The image survives environment.json.
		data, err := stack.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		back, err := stack.Unmarshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if got := back.Deployable("shop-api").Image; !reflect.DeepEqual(got, want["shop-api"]) {
			t.Errorf("%s: shop-api's image reads back as %+v", name, got)
		}
	}
	if s.Images[0].Packages[0] != "fonts-liberation" {
		t.Error("resolution sorted the stack's own list of packages")
	}
}

// TestImageRefusals: an element names a deployable of the stack that runs
// code, once, and adds something to its image. The cases that say what is
// wrong with an element's own packages or path put it in shop-api's place.
func TestImageRefusals(t *testing.T) {
	reg := assemble(t)
	for name, c := range map[string]struct {
		image ir.DeployableImage
		code  stack.Code
		parts []string
		// replace puts the element in the place of shop-api's own.
		replace bool
	}{
		"a database": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopDB), Packages: []string{"chromium"}},
			stack.CodeInvalidStack, []string{"images[4] (of shop-db) names database", "which runs no image"}, false,
		},
		"a bucket": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopMedia), Extend: "docker/media.Dockerfile"},
			stack.CodeInvalidStack, []string{"images[4] (of shop-media) names bucket shop-media, which runs no image"}, false,
		},
		"a deployable twice": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopAPI), Packages: []string{"tini"}},
			stack.CodeInvalidStack, []string{"images[0] and images[4] both name server shop-api; a deployable's image is named once"}, false,
		},
		"an API a declared server serves": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopOrders), Packages: []string{"tini"}},
			stack.CodeInvalidStack, []string{"images[1] and images[4] both name server Orders"}, false,
		},
		"a worker the API does not declare": {
			ir.DeployableImage{Of: stacktest.WorkerOf(stacktest.ShopOrders, "PrintLabels"), Packages: []string{"tini"}},
			stack.CodeUnknownDeployable, []string{"names worker PrintLabels of shop-orders, which declares no such @worker class"}, false,
		},
		"a service outside the stack": {
			ir.DeployableImage{Of: stacktest.Of(ir.ServiceRef{Name: "shop-billing", Kind: ir.SchemaKindAPI}), Packages: []string{"tini"}},
			stack.CodeUnknownService, []string{"names service shop-billing, which is not among the stack's services"}, false,
		},
		"nothing to add": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopAPI)},
			stack.CodeInvalidStack, []string{"names no packages and no Dockerfile to extend with"}, true,
		},
		"a package with an option": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopAPI), Packages: []string{"--allow-unauthenticated"}},
			stack.CodeInvalidStack, []string{`package "--allow-unauthenticated" is no Debian package name`}, true,
		},
		"a package twice": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopAPI), Packages: []string{"tini", "tini"}},
			stack.CodeInvalidStack, []string{"package tini is named twice"}, true,
		},
		"an absolute path": {
			ir.DeployableImage{Of: stacktest.Of(stacktest.ShopAPI), Extend: "/etc/Dockerfile"},
			stack.CodeInvalidStack, []string{`extend is "/etc/Dockerfile"; it is the path of a Dockerfile under the repository root`}, true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, services := imageShop()
			image := c.image
			if c.replace {
				s.Images = s.Images[1:]
			}
			s.Images = append(s.Images, &image)
			_, errs := resolve(t, reg, s, services, "Staging")
			mustFail(t, errs, c.code, c.parts...)
			if _, err := stack.Images(stack.Input{Stack: s, Services: services}); err == nil {
				t.Error("Images read the stack's images without the refusal")
			}
		})
	}
}
