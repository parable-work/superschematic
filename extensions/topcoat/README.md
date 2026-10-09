# extensions/topcoat

The Topcoat extension for superschematic. For an API service whose server
is Rust, `outputs.topcoat` writes a crate that a
[Topcoat](https://github.com/tokio-rs/topcoat) app depends on. It sits
beside the API crate, at `<out>/topcoat/<service>`, and is named
`<rust_crate_prefix><service>-topcoat`. The crate:

- mounts the service's JSON API in the app's router, at `/api/{*rest}`,
  giving its routes the client's address as Topcoat reads it, by which
  their rate limits count clients;
- calls each operation in-process, from a page, a shard or a procedure, by
  its route's rules: the caller admitted as the route admits it (401, 403),
  the arguments checked as the router checks them (the same 400), then the
  implementation ([D43](../../docs/DECISIONS.md)). An operation whose
  route does something first that a call cannot has no call: a webhook
  (`@webhook`, `@hmacVerified`), which a third party calls and whose route
  checks its signature, and an operation the service mounts itself
  (`@manualRouteRegistration`);
- offers a guard per operation, `can_<operation>`, which admits the caller
  alone, and whose doc says why an operation has no call;
- mirrors each type an operation a page calls returns, and the types it
  nests, as a Topcoat record a page can hand the browser.
- reads and renders a form per input type an operation a page calls takes
  whose fields a form holds, by the input type's rules ([Forms](#forms)).
- lets browser code call each operation through a Topcoat procedure, its
  arguments and result records and a refusal a record it reads
  ([Procedures](#procedures)).
- renders each record as a description list and as a table, labeled by
  the schema's titles and its types' `@display`
  ([Display components](#display-components)).

It is a Go module of its own, as `extensions/gcp` and `extensions/pulumi`
are. Neither the core nor the installed `superschematic`, which links gcp
and pulumi, links it (D44). `cmd/superschematic-topcoat` is the core with
it linked:

```bash
go build -o superschematic-topcoat ./cmd/superschematic-topcoat
```

A project's own binary links it beside its other extensions the same way,
passing it to `cli.New`:

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

Records, forms, procedures and views are on by default. `forms: false`,
`procedures: false` and `views: false` each leave out their module;
`records: false` leaves out the records, and with them the procedures and
the views, which are built on records.

The crate needs the Rust server. A service whose server is in another
language skips `outputs.topcoat` and logs why, so one config can serve a Go
build and `build --api-language RUST`.

A binary without the extension refuses `outputs.topcoat`, as it refuses
any outputs key no generator claims. A project that also builds its
configs with such a binary, `superschematic` among them, lists its Topcoat
services in `superschematic.toml` instead. Such a binary never reads that
table:

```toml
[extension.topcoat]
services = ["shop-orders"]
```

A listed service gets the crate with every default when its server is
Rust. `outputs.topcoat` in its config, when present, wins.

## The app

The acme shop's `examples/acme-shop/topcoat` is a Topcoat app over
`shop-orders`, and the docs site's
[Pages with Topcoat](../../docs/src/content/docs/guides/topcoat.mdx)
guide walks it. Its `app` mounts the crate:

```rust
let implementations = implementations(shop, Arc::clone(&users));
let pages = IdentityPageAuthenticator::of(&implementations);
Router::builder()
    .discover()
    .app_context(users)
    .shop_orders(implementations, pages)
    .build()
```

`shop_orders` takes the API's `Implementations`, the same value the JSON
API serves, and a `PageAuthenticator` when an operation needs a caller.
The authenticator establishes a page's caller, here from the session the
shop's users sign in with ([Users and sessions](#users-and-sessions)); an
app whose users are its own writes one over a Topcoat session, say. The
API's own authenticator still decides whether the caller's permissions
cover an operation's, so pages and the JSON API agree.

The app must name the crate, by `use`-ing an item of it. Topcoat discovers
items through the linker, and a crate the app never names is not linked.

## Users and sessions

When the API's users are the core user model's (D50), its
`Implementations.authenticator` is the identity runtime's, and the crate
also offers `IdentityPageAuthenticator`, beside a `PageAuthenticator` of
the app's own. It reads a page's caller from the session the request
carries, the session cookie or a bearer token, as the JSON API reads it,
so a user who signs in through the mounted `/api/auth/login` is signed in
on every page:

```rust
let pages = IdentityPageAuthenticator::of(&implementations);
Router::builder()
    .discover()
    .fixture_user_routes_api(implementations, pages)
    .build()
```

A cookie on a request other than `GET`, `HEAD` or `OPTIONS` passes the
identity config's cross-origin check, as on the JSON API, and a refused
cookie is cleared on the page's response. Topcoat's own origin policy
runs first, for pages and the mounted API alike, so an origin the identity
config trusts must also be one the app's `OriginPolicy` trusts. The
crate's `identity-postgres` and `identity-sqlite` features turn on the API
crate's.

An API that serves no login route of its own, as `shop-orders`, whose
users sign in through another API's, signs a user in from a page: the
page calls the identity service's `login` with a cookie session, after
`check_cookie_login`, and appends `config().session_cookie(..)` to the
page's response. The acme shop's `/sign-in` does so.

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

A field marked `@uiHidden` is left out, and so is a secret one
(`Secret<T>`), since everything in a record reaches the browser. The user
model's operations add no records: the identity runtime serves them, and
they have guards but no in-process call, so login's session token never
reaches a record. A field whose name a record reserves (`clone`, `then`,
...) gets a trailing underscore. `From<types::X>` builds the record from
the API's value.

## Forms

The input type of an operation with an in-process call whose fields a
form holds (a string, a number, a boolean or an enum, each alone, declared
by the service itself) gets a form in `forms`:

- **`<Input>Form`** holds each field as the browser sends it
  (`Option<String>`), so it deserializes from Topcoat's `Form<T>`, and a
  refused form renders again exactly as it was sent.
- **`parse()`** reads the fields as the input type's JSON and parses it with
  the generated `parse_<type>` and its rules ([D14](../../docs/DECISIONS.md)),
  undeclared keys refused. A checkbox is true when sent. Each field's
  errors come back as `FormErrors`. `FormErrors::from_api` reads an
  operation's refusal the same way, so a form shows both.
- **`<input>_fields(form, errors)`** is a component that renders each field
  with its label, its value as sent, and its errors:
  - each field's input type: email, url, tel, number (`step="1"` for an
    integer) or checkbox, from the field's type; `<select>` for an enum;
  - the attributes its rules give it: `required`, `min`/`max`,
    `minlength`/`maxlength`, and `pattern`. A pattern is written only when
    a browser reads it as the server does: no group syntax, and nothing
    the HTML `v` flag refuses. It is never written on an email or URL
    input, which checks its own syntax.

  A browser checks those attributes before sending, and `parse` checks
  every rule again.

```rust
#[page(POST "/signup")]
async fn sign_up(cx: &Cx, Form(form): Form<SignupInputForm>) -> topcoat::Result<impl View> {
    let errors = match form.parse() {
        Ok(input) => match operations::account_sign_up(cx, AccountSignUpArgs { input }).await {
            Ok(_) => return Err(see_other("/welcome").into()),
            Err(err) => FormErrors::from_api(&err),
        },
        Err(errors) => errors,
    };
    Ok(view! {
        (StatusCode::UNPROCESSABLE_ENTITY)
        <form method="post">signup_input_fields(form: form, errors: errors)</form>
    })
}
```

An input type with a list, a map, an object, a union or any JSON value
gets no form, and the build log says why. A form submits through the
operation's in-process call, so the input of an operation without one, a
webhook's or one the service mounts itself, gets none either
([what has no procedure](#what-has-no-procedure)). `forms: false` in
`outputs.topcoat` leaves out the module.

## Procedures

Each operation a browser may call is a Topcoat procedure in `procedures`
([what has none](#what-has-no-procedure)), on a stable path:
`/_superschematic/<service>/<namespace>/<operation>`. The app's
`.discover()` registers them; registering one again with `.route` panics.

- **Arguments.** The procedure takes the operation's arguments as a
  record, `<Op>ArgsRecord`, with a field per argument and the input as its
  type's record. `to_args()` writes each field as the JSON a request
  carries and decodes it into the `Args` struct. A UUID or timestamp that
  does not parse is the router's 400.
- **Result.** It answers `Result<OutputRecord, ProblemRecord>`.
  `ProblemRecord` holds the route's status, code and detail, and each
  refused field's path, rule and message. A refusal reaches the browser as
  data; an `Err` of a procedure would reach it as a bare 500.
- **Body.** `call_<operation>(cx, args)` is the procedure's body as a plain
  function: the arguments decoded, then the operation called in-process by
  its route's rules. Rust code calls it, since a procedure itself is not
  callable from Rust.

Browser code imports a procedure and calls it with the arguments record
inside an event handler; see Topcoat's procedures.

Records are the arguments' and results' carriers, so `records: false`
leaves out the procedures too; `procedures: false` leaves out only them.

### What has no procedure

A procedure is a second route to its operation, one any browser reaches,
so it exists only where a browser's request can meet the route's rules:

- An operation without an in-process call has none: a webhook, signed or
  not, and an operation the service mounts itself.
- An operation whose route admits only a service caller
  (`@requireService`, [D37](../../docs/DECISIONS.md)) has none, since a
  browser holds no service credential. Its in-process call stays and
  applies the end-user step alone; whether it should refuse is still
  open.
  An `@allowService` operation keeps its procedure, since its route
  admits an end user too.

The doc of the operation's call, or of its guard, says why, and so does
the build log. The result of an operation without a call gets no record,
and its input no form. A type that only a left-out procedure's arguments
name gets no record either.

### Route controls

A procedure meets its route's `@rateLimit`, `@bodyLimit` and `@timeout`
as the route's request does, in the route's order, and answers each
refusal as the route does, as a `ProblemRecord`: 429 `too_many_requests`
(with `Retry-After`), 413 `payload_too_large`, 504 `gateway_timeout`.
`<service>(...)` puts a `ProcedureControls` layer on the path of each
such procedure:

- **Rate limit.** The procedure has its own limiter at the route's rate,
  which counts each client by its IP address as Topcoat reads it
  (`client_ip`: the peer's, or the one a trusted proxy names). The JSON
  API's route keeps its own, so a client gets the rate on each, as it
  would on two replicas; `<service>(...)` gives the JSON API's routes the
  same address, so both count the same clients.
- **Body limit.** The procedure's request body is read up to the limit
  before its arguments are decoded, and Topcoat's own limit is raised to
  it for that procedure.
- **Timeout.** It covers the arguments' decoding and the call.

A control a procedure could not apply as its route does would leave the
procedure out, with the reason, rather than be skipped; all three apply.
An in-process call from the app's own page applies none of them: the
page is a route of the app, and its controls are the app's to set.

## Display components

`views` has two components per record, which a page renders as it
renders any component:

- **`<type>_detail(record: <Type>Record)`** renders a record as a
  description list: a `<dt>` label and a `<dd>` value per field, each in a
  `<div data-field="<json key>">`, inside
  `<div class="ss-detail" data-type="<Type>" role="group">`.
- **`<type>_table(rows: Vec<<Type>Record>)`** renders records as a
  `<table class="ss-table">`: a header row of labels, then a row per
  record, each cell `data-field="<json key>"`.

A procedure's argument record has them too. A type without a record has
none: a webhook's result, say ([what has no procedure](#what-has-no-procedure)).

```rust
#[page("/orders")]
async fn orders(cx: &Cx) -> topcoat::Result<impl View> {
    let args = OrderListOrdersArgs { statuses: None, limit: None };
    let orders = operations::order_list_orders(cx, args).await?;
    let rows: Vec<OrderViewRecord> = orders.iter().map(OrderViewRecord::from).collect();
    Ok(view! { order_view_table(rows: rows) })
}
```

A field is labeled as a form labels it: its `@docs` title, else its name
in words. A value renders by its field's type:

| The type's field | Renders as |
| --- | --- |
| A string, a UUID, a number | its text |
| An enum | `<data value="on_hold">On hold</data>`: the member's name in words, which `<enum>_label` gives |
| `Temporal.DateTime`, `Temporal.Date`, `Temporal.Time` | `<time datetime="...">` with the text the API sends |
| A boolean | "Yes" or "No" |
| An object type | its detail; in a table, its title when its type declares one |
| A list of objects | its type's table |
| Any other list | `<ul class="ss-list">` |
| A map | `<dl class="ss-map">` of its entries |
| A union, or any JSON value | its JSON text in `<pre class="ss-json">` |
| Optional, absent | nothing: an empty `<dd>` or `<td>` |

The type's `@display` ([D48](../../docs/DECISIONS.md)) shapes both:
`summaryFields` chooses and orders a table's columns (else every field),
`titleField` names a detail (`aria-label`, falling back to `noun`) and
heads each row (`<th scope="row">`), and `plural` captions a table.

Every value is text the view escapes, never markup. The components ship
no CSS and no inline styles: a stylesheet styles them by their `ss-`
classes and `data-field` attributes, and fills an empty value with
`:empty`. A type that nests itself boxes its components' views, as
Topcoat requires of a recursive component.
