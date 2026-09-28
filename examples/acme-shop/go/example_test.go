package shop_test

import (
	"context"
	"log"
	"net/http"
	"os"

	shopapi "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	"example.com/acme/shop"
	"go.uber.org/zap"
)

// Example wires the service to Postgres. It has no Output comment, so
// go test compiles it and does not run it.
func Example() {
	ctx := context.Background()
	db, err := orm.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	handler, err := shop.NewHandler(db, zap.NewExample(), shop.Auth{
		Validate:   verifyJWT,
		Sessions:   shopapi.NewSessionStore(db),
		Principals: shopapi.NewPrincipalStore(db),
		Roles:      staffRoles{},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8080", handler))
}
