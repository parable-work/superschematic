# extensions/topcoat

The Topcoat extension for superschematic. For an API service whose server
is Rust, `outputs.topcoat` writes a crate that a
[Topcoat](https://github.com/tokio-rs/topcoat) app depends on. It sits
beside the API crate, at `<out>/topcoat/<service>`, and is named
`<rust_crate_prefix><service>-topcoat`. The crate:

- mounts the service's JSON API in the app's router, at `/api/{*rest}`;
- calls each operation in-process, from a page, a shard or a procedure, by
  its route's rules: the caller admitted as the route admits it (401, 403),
  the arguments checked as the router checks them (the same 400), then the
  implementation ([D43](../../docs/DECISIONS.md));
- offers a guard per operation, `can_<operation>`, which admits the caller
  alone;
- mirrors each type an operation returns, and the types it nests, as a
  Topcoat record a page can hand the browser.

It is a Go module of its own, as `extensions/gcp` and `extensions/pulumi`
are, and the core binary does not link it. A binary that does is the core
with the extension passed to `cli.New`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/extensions/topcoat"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic-topcoat"}, topcoat.Extension{}).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

## The config

```ts
outputs: {
  types: { [TargetLanguage.Rust]: { enabled: true } },
  api: { enabled: true, language: "RUST" },
  topcoat: { enabled: true },   // records: false leaves the records out
}
```

The crate needs the Rust server. A service whose server is in another
language skips `outputs.topcoat` and logs why, so one config can serve a Go
build and `build --api-language RUST`.

## The app

```rust
use schemas_shop_orders_topcoat::api::{types, OrderPlaceOrderArgs};
use schemas_shop_orders_topcoat::{operations, RouterBuilderShopOrdersExt};

let router = Router::builder()
    .discover()
    .shop_orders(implementations, SessionCaller)
    .build();

#[page(POST "/orders")]
async fn place_order(cx: &Cx) -> topcoat::Result<impl View> {
    let args = OrderPlaceOrderArgs { input: types::PlaceOrderInput { /* ... */ } };
    let order = operations::order_place_order(cx, args).await?;
    Ok(view! { <p>"Order " (order.id.to_string()) " placed"</p> })
}
```

`shop_orders` takes the API's `Implementations`, the same value the JSON
API serves, and a `PageAuthenticator` when an operation needs a caller.
The authenticator establishes a page's caller, typically from the app's
Topcoat session. The API's own `Authenticator` still decides whether the
caller's permissions cover an operation's, so pages and the JSON API agree.

The app must name the crate, by `use`-ing an item of it. Topcoat discovers
items through the linker, and a crate the app never names is not linked.

## Records

A record holds the type's JSON as the API sends it, in the types a record
can hold:

| The type's field | The record's |
| --- | --- |
| A string, a UUID, a timestamp or an enum | `String` |
| An integer scalar | `i64` |
| A number | `f64` |
| A boolean | `bool` |
| An object type | its record |
| A list, or a list of lists | `Vec` |
| A map | `Vec<(String, T)>` |
| Optional | `Option` |
| A union, or any JSON value | its JSON text |

A field marked `@uiHidden` is left out, since everything in a record
reaches the browser. A field whose name a record reserves (`clone`,
`then`, ...) gets a trailing underscore. `From<types::X>` builds the
record from the API's value.
