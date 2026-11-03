# extensions/topcoat

The Topcoat extension for superschematic. For an API service whose server
is Rust, `outputs.topcoat` writes a crate that a
[Topcoat](https://github.com/tokio-rs/topcoat) app depends on. It sits
beside the API crate, at `<out>/topcoat/<service>`, and is named
`<rust_crate_prefix><service>-topcoat`. The crate:

- mounts the service's JSON API in the app's router, at `/api/{*rest}`,
  giving its routes the client's address as Topcoat reads it, by which
  their rate limits count clients, and their limiters from the app's one
  registry, which the procedures and the pages share, so a client has one
  budget per operation through its route, its procedure and the pages
  ([Route controls](#route-controls)); an in-process call, the app's own
  or a service's, meets no traffic control;
- calls each operation in-process, from a page, a shard or a procedure, by
  its route's rules: the caller admitted as the route admits it (401, 403),
  the arguments checked as the router checks them (the same 400), then the
  implementation ([D43](../../docs/DECISIONS.md)). An operation whose
  route does something first that a call cannot has no call: a webhook
  (`@webhook`, `@hmacVerified`), which a third party calls and whose route
  checks its signature, and an operation the service mounts itself
  (`@manualRouteRegistration`). An operation only a service may call
  (`@requireService`) has no end user's call: `operations::as_service`
  calls it, and every operation with a service clause, as a service the
  app names ([Calling as a service](#calling-as-a-service));
- offers a guard per operation, `can_<operation>`, which admits the caller
  alone, and whose doc says why an operation has no call;
- mirrors each type an operation a page calls returns, and the types it
  nests, as a Topcoat record a page can hand the browser.
- reads and renders a form per input type an operation a page calls
  takes, nested objects and lists included, by the input type's rules,
  and per operation a page submits with arguments, path, query and body,
  by the router's rules, a GET's a filter read from the query
  ([Forms](#forms)).
- lets browser code call each operation through a Topcoat procedure, its
  arguments and result records and a refusal a record it reads
  ([Procedures](#procedures)).
- renders each record as a description list and as a table, labeled by
  the schema's titles and its types' `@display`, a time in words and an
  id by the name the app's choices give it
  ([Display components](#display-components)).
- pages a `@paginated` operation's call, form, procedure and list (D60),
  offers what a `RefersTo` value refers to as a select and names it by
  its item's label, from the operation that lists the items (D61), and
  renders a `SearchQuery` argument as a search box (D62)
  ([Pages, references and search](#pages-references-and-search)).
- when asked, serves a page per resource the operations describe, under a
  prefix, with a layout, a nav and an index, each page under its
  operation's route controls, and, for an app that loads Topcoat's
  runtime, lists whose filter updates them in place ([Pages](#pages)).
  Beside them it pages the engine's types, when the API's database is an
  engine database ([Engine pages](#engine-pages)), and signs a caller in
  and out and administers the users and roles, when its users are the
  core user model's ([Sign-in and admin pages](#sign-in-and-admin-pages)).
- shows an amount whose field names its currency, `@display({ currency })`,
  as money, `GBP 4.50`, and takes it in a form as a decimal
  ([Money](#money)).

It is a Go module of its own, as `extensions/gcp` and `extensions/pulumi`
are. Neither the core nor the installed `superschematic`, which links the
official extensions (gcp, cloudflare and pulumi), links it (D44).
`cmd/superschematic-topcoat` is the installed binary with it linked
beside those (D44, amended): it takes the same list from
`cmd/superschematic/official`, so it builds and deploys everything
`superschematic` does, and builds a stack whose app names `topcoat`
(D78), which `superschematic` refuses. Each release ships it beside
`superschematic`, built the same way against the release's static
archives, as `superschematic-topcoat_<version>_<platform>.tar.gz` in the
same `SHA256SUMS` (D47, amended), and a stack's generated CI installs it
for a services root with such an app. From a checkout:

```bash
go build -o superschematic-topcoat ./cmd/superschematic-topcoat
```

A binary built from a checkout names no release, so the workflow it
writes for a stack cannot install one: write it with the released
binary. `go install` refuses the command, since the module replaces its
siblings by their directories.

A project's own binary links it beside the official extensions, or its
own, the same way, passing them to `cli.New`:

```go
package main

import (
	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/cmd/superschematic/official"
	"github.com/parable-work/superschematic/extensions/topcoat"
)

func main() {
	if err := cli.New(cli.Config{Name: "superschematic-topcoat"}, append(official.Extensions(), topcoat.Extension{})...).Execute(); err != nil {
		cli.Exit(err)
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

Records, forms, procedures and views are on by default. `forms: false`
leaves out both kinds of form; `procedures: false` and `views: false` each
leave out their module;
`records: false` leaves out the records, and with them the procedures and
the views, which are built on records. Pages are off by default:
`pages: { enabled: true, prefix: "/console" }` turns them on, and they
need the records, the forms and the views ([Pages](#pages)); `live: true`
beside them renders each list with a filter through a shard ([Live
lists](#live-lists)). Beside `enabled`, `engine: false` leaves out the
[engine pages](#engine-pages), and `identity: false` the [sign-in and
admin pages](#sign-in-and-admin-pages); `identity: { signUp: true }`
adds a sign-up page. Both are on by default wherever they apply.

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
Rust. `outputs.topcoat` in its config, when present, wins. The services
the table lists are those one app hosts (D61, amended): a listed
service's reference whose options are a listed API dependency's calls
that dependency's crate ([Several services in one
app](#several-services-in-one-app)). The table's `pages_prefix` also turns
on the listed service's pages, under it:

```toml
[extension.topcoat]
services = ["shop-orders"]
pages_prefix = "/console"
```

A path serves one listed service. With more than one, `pages_prefix` is a
table of the services whose pages are on, each with its own prefix, and a
service it does not name has none:

```toml
[extension.topcoat]
services = ["shop-orders", "shop-catalog"]
pages_prefix = { "shop-orders" = "/console/orders", "shop-catalog" = "/console/catalog" }
```

`pages_live = true` beside `pages_prefix` makes the lists of the pages it
turns on live, as `pages.live` would ([Live lists](#live-lists));
`pages_engine = false` and `pages_identity = false` turn off the engine
pages and the identity pages, and `pages_identity = { sign_up = true }`
adds sign-up. Without a `pages_prefix` the table refuses each of them.

An app that links two crates registers each one's pages, layout and
index at its prefix, so the table refuses one path for several services,
which would register a page twice, as Topcoat's router refuses, and a
prefix that is, or is under, another's, which would put one console's
layout around the other's pages. It refuses a service `services` does
not list too.

## The app

The acme shop's `examples/acme-shop/topcoat` is a Topcoat app over
`shop-orders`, and the docs site's
[Pages with Topcoat](../../docs/src/content/docs/guides/topcoat.mdx)
guide walks it. Its `app` mounts the crate, and, given the asset bundle,
serves Topcoat's runtime, which the console's [live lists](#live-lists)
render in place with:

```rust
let implementations = implementations(shop, Arc::clone(&users));
let authenticator = IdentityPageAuthenticator::of(&implementations);
let builder = Router::builder()
    .discover()
    .app_context(users)
    .app_context(console_settings())
    .shop_orders(implementations, authenticator);
match assets {
    Some(assets) => builder.assets(assets).runtime().build(),
    None => builder.build(),
}
```

`shop_orders` takes the API's `Implementations`, the same value the JSON
API serves, and a `PageAuthenticator` when an operation needs a caller.
It makes one registry of the operations' rate limiters, which the JSON
API's routes, the procedures and the pages take theirs from, and keeps it
in the app context as `Setup.rate_limits`: an app that mounts the API's
router again (`RouterOptions { rate_limits: setup.rate_limits.clone(), ..
}`), or a page of another crate that calls an operation
(`setup.rate_limits.of(&api::operations::ORDER_PLACE_ORDER)`), shares the
operation's budget by taking from it too. The page counts its client by
the crate's `client_key(cx)`, the key the routes, the procedures and the
pages count by, so the client is one client on every way in:

```rust
use acme_shop_orders_topcoat::{api, client_key, Setup};
use topcoat::context::app_context;

let setup: &Setup = app_context(cx);
// None for an operation without a @rateLimit.
if let Some(limiter) = setup.rate_limits.of(&api::operations::ORDER_PLACE_ORDER)
    && let Err(retry_after) = limiter.take(&client_key(cx))
{
    // The client's budget is spent: answer 429 with Retry-After.
}
```

`console_settings()` is the [pages](#pages)' `pages::Settings`, since
acme's `superschematic.toml` turns shop-orders' console on, with its lists
live (`pages_live`). Its root layout, `#[layout("/")] document`, gives
every page its HTML document, with the runtime's script in its head when
the router set the runtime up; `main` loads the bundle with
`AssetBundle::load()` and, without one, serves the pages without the
runtime.
The authenticator establishes a page's caller, here from the session the
shop's users sign in with ([Users and sessions](#users-and-sessions)); an
app whose users are its own writes one over a Topcoat session, say. The
API's own authenticator still decides whether the caller's permissions
cover an operation's, so pages and the JSON API agree.

The app must name the crate, by `use`-ing an item of it. Topcoat discovers
items through the linker, and a crate the app never names is not linked.

### The engine

When the API's database is an engine database, a DB service whose types
compose behaviors (D58), the crate keeps the engine in-process on SQLite.
`engine::open(path)` opens it on a SQLite file, created if absent, with
the database's engine document (its Rust types' `ENGINE_DOCUMENT`), the
access policy its config sets (`engine::POLICY`) and the behaviors of the
core or of the project's engine crate, and defines and publishes the
document's schemas. The setup method then takes the engine, mounts its
routes, its event stream and its MCP endpoint at `/engine/{*rest}`, each
request's caller the one the API's `Authenticator` establishes, and keeps
it in the app context (`Setup::engine`), which a page calls in-process on
the blocking pool:

```rust
let engine = notes_api_topcoat::engine::open("notes.engine.db")?;
Router::builder()
    .discover()
    .notes_api(implementations, authenticator, engine)
    .build()
```

The crate depends on the engine's crates at the naming file's `[paths]
engine_rust` and `engine_behaviors_rust`; without them it mounts no
engine, and the build says so. `<service>_without_api` (below) takes the
engine too and keeps it for the pages, but mounts none of its routes, as
it mounts no JSON API.

With the pages on, the console pages the engine's types too, each
instance's behaviors in panels and its operations as forms ([Engine
pages](#engine-pages)).

### Several services in one app

`[extension.topcoat] services` lists the services one Topcoat app hosts
(D61, amended). Each crate's `<service>(implementations, page_authenticator)`
mounts its JSON API at `/api/{*rest}`, and Topcoat refuses a second route
there as the app builds ("duplicate any-method route registered"), so an
app sets up one crate that way and every other with
`<service>_without_api(implementations, page_authenticator)`, which keeps
the implementations for the in-process calls, makes the crate's own
registry of limiters and adds its procedures', pages' and pickers'
layers, and mounts no JSON API. The app serves at most one service's JSON
API at `/api`:

```rust
Router::builder()
    .discover()
    .fixture_console_api(console, Clerk)
    .fixture_console_suppliers_api_without_api(suppliers, Clerk)
    .build()
```

Each call reads its crate's `Setup` from the app context. A crate the app
links but does not set up answers each call with a 500, `the app does not
host fixture-console-suppliers-api: its router builder calls neither
fixture_console_suppliers_api nor fixture_console_suppliers_api_without_api`,
not a panic, so a page whose reference calls it shows the plain control
and the value. An app links every crate the crates it names depend on,
and `.discover()` registers their pages and procedures, set up or not.

When the table lists both a service and an API dependency of it, a
reference whose options operation is the dependency's ([What a value
refers to](#what-a-value-refers-to)) calls the dependency's crate
in-process: the service's crate depends on it, `path = "../<dependency>"`,
and calls `<crate>::operations::<operation>`, by the dependency's rules
and under its setup. The dependency's crate must be built first, as
`build --with-deps` and `build-all` build it. The extension reads a
dependency's Rust API only when a reference's options are its operation,
so a dependency no reference calls is never read and fails no build. A
reference whose dependency cannot be hosted is left out, with the reason
in the build log: the table does not list both, the dependency's config
generates no API server, its server is not Rust, its config or its API
cannot be read, or its crate is not built. When the service's API and the
dependency's both authenticate with the identity runtime, the crate's
`identity-postgres` and `identity-sqlite` features turn on the hosted
crate's too ([Users and sessions](#users-and-sessions)).

### An app in a stack

An API service may declare an app, `@app({ extensions: ["topcoat"] })` on
an empty class of its schema, which a stack runs as a deployable of its
own beside the API's servers (D78). The app's program is a library crate
the project writes, at the naming file's `[implementation_paths]
rust_app` (`apps/{app}` by default), exporting

```rust
pub async fn serve(parts: <api crate>::apps::<App>Parts) -> std::io::Result<()>
```

which the app's generated entrypoint calls once it has built the parts
as the API's server builds its own: the API's `deps`, its
`implementations`, the `engine` of an engine database, open on the
stack's database, and `addr`, every interface at `PORT`. The core writes
no Topcoat code for it, and a binary that does not link this extension
refuses to build the stack: run `superschematic-topcoat`.

For an API that declares an app, the crate gains
`<service>_with_parts(parts)` and `<service>_with_parts_and_api(parts)`,
which take any app's parts (through `AppParts`, which each `<App>Parts`
converts into) and set the crate up from them: the implementations, the
engine the entrypoint opened, in place of `engine::open` on SQLite, and,
when the API authenticates with the identity runtime,
`IdentityPageAuthenticator::of(&parts.implementations)`. An API whose
users are its own passes its `PageAuthenticator` after the parts.
`_with_parts` serves the pages alone, which call the API and the engine
in-process, as `<service>_without_api` does: a stack exposes an app apart
from its API, so the app's address does not serve an API the stack keeps
internal. `_with_parts_and_api` mounts the JSON API at `/api` and the
engine's routes at `/engine` too, as `<service>` does, for an app whose
API the stack exposes as well. `serve` binds `parts.addr` and hands the
listener to `topcoat::serve`:

```rust
pub async fn serve(parts: BackofficeParts) -> std::io::Result<()> {
    let listener = tokio::net::TcpListener::bind(parts.addr).await?;
    let router = Router::builder().discover().shop_stock_with_parts(parts).build();
    topcoat::serve(listener, router).await
}
```

Never `topcoat::start`, which binds `HOST` and `PORT`, `127.0.0.1:3000`
by default: on Cloud Run the app would listen on the container's loopback
and fail its startup probe. The app's crate depends on this crate by
path under the output root; the stack's build copies the app's crate
into the output root's `impl/` with its paths re-based, and its image
takes in every crate it reaches by path, this one among them. The image
carries no asset bundle yet, so a live list is a GET form there.
acme's `examples/acme-shop/apps/backoffice` is such an app over
`shop-stock`, with its pages under `/stock` and the engine console over
`shop-db` at `/stock/engine`; `superschematic-topcoat stack dev
schemas/services/shop-stack` runs it beside `shop-stock`'s server.

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
crate's, and those of each hosted crate a reference calls whose API has
them ([Several services in one app](#several-services-in-one-app)).

An API that serves no login route of its own, as `shop-orders`, whose
users sign in through another API's, signs a user in from a page: the
page calls the identity service's `login` with a cookie session, after
`check_cookie_login`, and appends `config().session_cookie(..)` to the
page's response. The acme shop's `/sign-in` does so. With the pages on,
the crate writes such a page itself, with sign-out, an account page and
the users and roles ([Sign-in and admin pages](#sign-in-and-admin-pages)).

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

A `@paginated` operation's page (D60) is a page record per element type,
`<Element>PageRecord { items, next }`: its items as a record holds the
element, `OrderViewRecord` or `String`, and `next`, the cursor of the next
page, none after the last. `From<&Page<types::X>>` builds it. A schema
that declares a type named `<Element>Page`, whose record it would be too,
fails the build.

## Forms

`forms` has two kinds of form, which share their controls, their
decoding and their errors:

- an **input form**, `<Input>Form`, per input type an operation with an
  in-process call takes, declared by the service itself, parsed by the
  input type's rules ([D14](../../docs/DECISIONS.md)). The user model's
  operations have no in-process call, so login's input and the
  administration routes' arguments get none: a page signs a user in
  through the identity service, as above;
- an **argument form**, `<Ns><Op>ArgsForm`, per operation a form submits:
  a GET operation with a query argument, whose form is a filter (a GET
  whose path carries its arguments alone is a link, and gets none), and
  any other operation with an argument beside its input (one whose only
  argument is its path's is a button). It holds each argument, embeds the
  input's form when the operation takes one, and is parsed by the
  router's own rules (`Args::check`, [D43](../../docs/DECISIONS.md)).

A form holds each field as the browser sends it, so a refused form
renders again exactly as it was sent. A control is named by its field's
path: `email`, a nested object's `shippingAddress.city`, a row's
`lines[0].productId`, a list of values' `tags[0]`, and an argument by its
name, beside its input's fields. It is labeled by the field's `@docs`
title, else by its name in words, in which a number that ends a word is
a word of its own (`line1` is "Line 1", `addressLine2` "Address line 2")
unless an acronym or a single letter holds it (`awsS3Bucket` is "Aws S3
bucket"). An enum's member, a select's option or a checkbox's label, is
labeled by its title (`/** @title Held for a stock check */` in
TypeScript, `title` in the data forms, [D48](../../docs/DECISIONS.md)),
else by its name in words: `OnHold` is "On hold". When two members of an
enum the crate labels take one label, ignoring case, such as `OnHold` and
`Paused` titled "On hold", the build log says so, and a `@title` tells
them apart: `topcoat: enum Status: members OnHold and Paused share the
label "On hold", so a select offers it twice; a @title tells them apart`.

### Input forms

- **`<Input>Form`** holds a value as `Option<String>`, a nested object as
  its type's own `<Type>Form`, a list of objects as a `Vec` of them, a list
  of values as `Vec<Option<String>>` and a list of an enum's members as
  `Vec<String>`, and `overflow`, true when the post sent more than the
  form reads ([Decoding](#decoding)). `new()` is a new form: each field's
  `@default`, a required nested object's new form, and each list's first
  rows (its `listMin`, or one when the input requires the list).
  `Default` is the empty form.
- **`parse()`** writes the fields as the input type's JSON and parses it
  with the generated `parse_<type>`, undeclared keys refused. A checkbox
  is true when sent, a blank control is no value, and a blank row of a
  list of values is null, which the rules refuse at the row.
- **`<input>_fields(form, errors, choices)`** renders each field, every
  prop optional (`form` defaults to `new()`): a nested object as a
  `<fieldset class="ss-object">` with its label as `<legend>`; a list of
  objects as a `<fieldset class="ss-list">` of rows, each a
  `<fieldset class="ss-row">` numbered by the row type's `@display` noun
  (else the list's label); a list of values as a control per row; and a
  list of an enum's members as a `<fieldset class="ss-choices">` of
  checkboxes. An optional nested object is sent only when one of its
  fields is, so none of its controls is `required`, and it starts empty.

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
        <form method="post">
            signup_input_fields(form: form, errors: errors)
            <button type="submit">"Sign up"</button>
        </form>
    })
}
```

### Argument forms

- **`<Ns><Op>ArgsForm`** holds each argument as an `Option<String>`, a
  list's every value as a `Vec<String>`, the input's form as `input`
  when the operation takes one, and `overflow`. `METHOD` is `"get"` for a
  filter and `"post"` for any other. `new(<path arguments>)` is the form
  of one resource, its path arguments hidden inputs the page fills (the
  order's id, say), each argument's declared default, and the input's new
  form.
- **`parse()`** writes each argument as the JSON a request carries,
  decodes it into the operation's `Args` struct, parses the input by its
  form, and runs `Args::check`, so a value the JSON API would refuse with
  400 is that argument's error. A blank optional argument is absent and
  one with a declared default reads it; an optional input is absent when
  none of its fields was sent, and parsed by its rules when one was;
  `check` refuses the first argument that breaks a rule. **`submit(cx)`**
  parses the form and makes the in-process call
  (`operations::<ns>_<op>`). Its refusal is a `Refusal`, the status a
  page answers with and the errors it renders the form again with: 422
  and `parse`'s errors, or the route's status (401, 403, 404, 409, ...)
  and the operation's ([Errors](#errors)).
- **`<ns>_<op>_args_fields(form, errors, choices)`** renders each argument
  with the control an input field of its type gets, labeled by its name in
  words, its description a hint (`aria-describedby`), then the input's
  fields. A list of an enum is a group of checkboxes; any other list an
  input per value sent and a blank one for another; a path argument a
  hidden input.
- A **filter** (a GET's form) reads the query: `Form<T>` reads it on GET,
  and `from_query(cx)` outside an extractor, so the controls show the
  filters in effect. A filter's boolean is a `<select>` of Yes (`true`)
  and No (`false`), after a blank option when the filter may leave it
  out, which reads its declared default or filters nothing; a new form
  selects the default. A checkbox could not say false, nor turn off a
  flag whose default is true, since an unchecked box sends nothing.
- Any other form's boolean is a **checkbox**, as an input's is: checked is
  true and unchecked false, whatever the argument's default, which only
  checks a new form's box when it is true.

A page whose route holds a path argument fills it from the route before
`submit`, rather than trusting the hidden input, which a reader can change.
The page answers with the refusal's status, so a refused caller gets the
route's 401 or 403, and a cancel the operation refuses its 409, as the JSON
API would answer:

```rust
path_param!(id: types::IdentityUUID, error = not_found);

#[page(POST "/orders/{id}/cancel")]
async fn cancel(cx: &Cx, Form(form): Form<OrderCancelOrderArgsForm>) -> topcoat::Result<impl View> {
    let form = OrderCancelOrderArgsForm { id: Some(path_param::<Id>(cx)?.to_string()), ..form };
    let refused = match form.submit(cx).await {
        Ok(_) => return Err(see_other("/orders").into()),
        Err(refused) => refused,
    };
    Ok(view! {
        (refused.status)
        <form method="post">
            order_cancel_order_args_fields(form: form, errors: refused.errors)
            <button type="submit">"Cancel the order"</button>
        </form>
    })
}

#[page("/orders")]
async fn orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let (status, orders, errors) = match filter.submit(cx).await {
        Ok(orders) => (StatusCode::OK, orders, FormErrors::default()),
        Err(refused) => (refused.status, Vec::new(), refused.errors),
    };
    let rows: Vec<OrderViewRecord> = orders.iter().map(OrderViewRecord::from).collect();
    Ok(view! {
        (status)
        <form method=(OrderListOrdersArgsForm::METHOD)>
            order_list_orders_args_fields(form: filter, errors: errors)
            <button type="submit">"Filter"</button>
        </form>
        order_view_table(rows: rows)
    })
}
```

### Decoding

`Form<T>` decodes either kind, a post's body or a GET's query, as does
`T::from_pairs(pairs)`: the pairs become a tree of their names. A list's
rows are read in the order of their indexes, so a gap or a removed row
closes up. An input field takes the last value sent (a checkbox row sends
a hidden input of its name first), and an argument the first, as the
router reads a parameter sent twice. A list argument's values are each
value sent, a query list's split on commas as the router splits it. A
blank value is none, and a name no control has is ignored.

A form reads what a browser could send it and little more, so a post of
more names than any form has costs no more than a form does:

- **`MAX_PAIRS`, 5000**: a form reads at most this many names and
  values, and none after them. `overflow` says it was sent more, and
  `parse` refuses the form with the form's own message.
- **A list's limit**: its `listMax`, or **`MAX_ROWS`, 1000**, for a list
  without one or with one above it. A form reads one row past the limit
  and no more, and `parse` refuses a list past it at the list ("must
  contain at most 3 items"). So a post of 4,000 rows to a list of at most
  3 renders again with 4 rows and that message, and one of 50,000 with 4
  rows and the form's.

### Controls

A control is typed by its field's or argument's type, and carries the
attributes its rules give it:

- email, url, tel, number (`step="1"` for an integer) or checkbox, and a
  `<select>` for an enum. A number input sends what a reader typed, so an
  integer reads any whole number in an i64's range, `1e1` and `2.0`
  too;
- `type="date"` for `Temporal.Date` and `type="time"` for
  `Temporal.Time`; `type="datetime-local"` for `Temporal.DateTime`, which
  carries no offset, so the form reads it as UTC
  (`2026-10-09T14:30` is `2026-10-09T14:30:00Z`) and its label ends in
  "(UTC)". A value held with an offset renders in UTC
  (`2026-10-09T14:30+01:00` as `2026-10-09T13:30`), with its seconds and
  up to three digits of their fraction, and the control takes any step
  (`step="any"`), so a held value with seconds is not refused;
- `type="password"` for a `@secret` field, never rendered with its value:
  a form holds what a reader types, though a record leaves the field out;
- a `<textarea rows="6">` for long text, a text field or argument whose
  `maxLength`, its own or its scalar's, is **500** or more: a reason, a
  review's body, not a name or a title. A field's multiline hint,
  `@display({ multiline })` ([D48](../../docs/DECISIONS.md)), overrides
  that rule either way: `true` makes text a textarea without a bound or
  under 500, a note, and `false` keeps it one line at any bound, a code.
  The hint applies where the control would be text: an email, URL,
  phone, date or time control keeps its type, and the build log says so
  (`topcoat: Account.email: @display multiline is ignored: its control is
  an email input`). An argument takes no decorator, so it keeps the 500
  rule; an argument form's input fields follow their own hints. A
  textarea takes `minlength` and `maxlength`, but no `pattern`, which
  `parse` checks, and a newline after its start tag, which a browser
  drops, so a value that starts with one renders as sent;
- for an amount in a literal currency, `@display({ currency: "GBP" })`, a
  decimal control labeled with its code, and for one whose currency is
  another field a whole number of minor units ([Money](#money));
- a `<textarea>` of JSON text, "(JSON)" in its label, for a value no
  control holds: a union, any JSON value, a map, a list of lists, an
  argument's object or list of booleans, or a type that nests the type
  holding it;
- `required`, `min`/`max`, `minlength`/`maxlength`, and `pattern`. A
  pattern is written only when a browser reads it as the server does: no
  group syntax, and nothing the HTML `v` flag refuses. It is never written
  on an email or URL input, which checks its own syntax.

A browser checks those attributes before sending, and `parse` checks every
rule again. A value a control cannot hold (`seats=many`, JSON that does
not parse, a date that does not exist) is refused at the control.

### Errors

`FormErrors` holds each control's messages in a map by its name, so a
control finds its own, and the names under it, without reading every
other control's. `parse`'s errors,
and an operation's refusal through `FormErrors::from_api` (its `errors`,
nested for a nested object's fields, and a refused parameter the router's
400 names in its details), are read by path, so each message renders at
its control: a quantity over 99 on the second line is shown on
`lines[1].quantity`. `errors.of(name)` is a control's messages and
`errors.under(name)` a JSON value's or a list's with the place of each. A
message no rendered control shows, an `@uiHidden` field's or a path
argument's, renders with the form's own, after its name.

A `Refusal` is the status a page answers with and the errors it renders
the form again with. An argument form's `submit` returns one, and a page
over an input form, which knows its operation, builds one:
`Refusal::from(errors)` is `parse`'s refusal, 422, and
`Refusal::from_api(&err)` the operation's, with the route's status (401,
403, 404, 409, ...) and `FormErrors::from_api(&err)`.

### Rows without JavaScript

A form whose input holds a list as rows has submit buttons named
`_action`: `add:lines` after a list's rows, unless it holds its limit
(its `listMax`, or `MAX_ROWS`), and `remove:lines[1]` in each row,
unless the list holds no more than its `listMin`. They carry
`formnovalidate`, so a row can be added before the others are valid. A
row's own list is named under the row: a room's notes add with
`add:rooms[0].notes` and remove with `remove:rooms[0].notes[1]`. The form
holds the button pressed in `row_action`. `apply_action()`, on the input
form and on an argument form that embeds it, applies it, numbering the
rows after a removed one again with their values and their pickers'
texts as sent, and is true when a row button submitted the form: the
page renders the form again, 200, without calling the operation. A
search picker's "Find" in a posted form is a row button too,
`find:lines[0].productId` ([A search picker](#a-search-picker)):
`apply_action()` records it as pressed in the form's `finds` and is true,
and the page renders the form again, its values as sent and each
picker's text searched, the pressed one's past the cap.

Enter in a text field presses a form's default button, its first submit
control in tree order, which must submit the form as such. So the
component renders a hidden submit button,
`<button type="submit" class="ss-submit" hidden>`, ahead of every row
button and every picker's "Find", and Enter submits the form with no
`_action`. A page puts its own
submit button after the component, inside the one `<form>` around it: a
form nests no other form, which the HTML parser would drop, and no
control names its form with `form=`, so tree order is the whole rule. The
extension's tests hold every page they render to it without a browser:
`testdata/enter.rs` checks that each form's first submit control has no
`name`, `formaction`, `formmethod`, `formenctype`, `formtarget`,
`formnovalidate` or `disabled`, and no `<fieldset disabled>` around it
outside that fieldset's first `<legend>`. It reads a `<template>`'s
contents as a fragment of their own, out of the page's forms, held to the
same rule. `TestRowButtonsFollowTheDefaultButton` reads every golden's
components for the hidden button's place without cargo. The file ships
with neither the extension nor the crate it writes, so an app's own
tests, with `http` among their dev-dependencies, hold its pages to the
rule by copying it from the superschematic repository
(`extensions/topcoat/testdata/enter.rs`) into their `tests/support/`,
declaring `#[path = "support/enter.rs"] mod enter;`, then calling
`enter::check_page(uri, &headers, &body)` on each answer. acme's tests
read the repository's copy by its path.

```rust
#[page(POST "/book")]
async fn book(cx: &Cx, Form(form): Form<BookingInputForm>) -> topcoat::Result<impl View> {
    let mut form = form;
    let (status, errors) = if form.apply_action() {
        (StatusCode::OK, FormErrors::default())
    } else {
        match form.parse() {
            Ok(input) => match operations::booking_book(cx, BookingBookArgs { input }).await {
                Ok(_) => return Err(see_other("/booked").into()),
                Err(err) => (StatusCode::UNPROCESSABLE_ENTITY, FormErrors::from_api(&err)),
            },
            Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, errors),
        }
    };
    Ok(view! {
        (status)
        <form method="post">
            booking_input_fields(form: form, errors: errors)
            <button type="submit">"Book"</button>
        </form>
    })
}
```

### Choices the app supplies

An input type does not say which values an id may take unless it refers
to an item (`RefersTo`, [What a value refers to](#what-a-value-refers-to)):
a plain `productId` is a UUID, not a product. The page says, with `Choices`
(`crate::choices`, re-exported from `forms`), which either kind of form
component takes, as the [display components](#display-components) do: a
text or number control it names renders as a `<select>` of the options,
after a blank one, and the value held stays an option when no choice is
it. A field is named by its path without the rows' indexes, for every
row, or by its control's name, for one row, which wins:

```rust
let choices = Choices::new()
    .with("rooms.roomId", rooms.iter().map(|room| (room.id.clone(), room.name.clone())))
    .with("rooms[1].roomId", [(attic_id, "Attic")]);
view! { booking_input_fields(form: form, choices: choices) }
```

### What has no form

An input type a dependency declares gets no form, nor does an operation
whose input has none or one with an argument named as one of its input's
fields, as its input's row buttons (`_action`) or as the form's
`overflow`, and the build log says why. An input with a field of a name
the form keeps for itself, `overflow`, or in a form with rows
`row_action` or the JSON name `_action`, fails the build, as does a form
with a search picker and a field or an argument named `finds`,
`row_action`, or by the JSON names `_find` and `_action`. A form submits
through the operation's in-process call, so an operation without one, a
webhook's or one the service mounts itself, gets none ([what has no
procedure](#what-has-no-procedure)). `forms: false` in `outputs.topcoat`
leaves out both kinds.

## Procedures

Each operation a browser may call is a Topcoat procedure in `procedures`
([what has none](#what-has-no-procedure)), on a stable path:
`/_superschematic/<service>/<namespace>/<operation>`. The app's
`.discover()` registers them; registering one again with `.route` panics.

- **Arguments.** The procedure takes the operation's arguments as a
  record, `<Op>ArgsRecord`, with a field per argument and the input as its
  type's record. `to_args()` writes each field as the JSON a request
  carries and decodes it into the `Args` struct. A UUID or timestamp that
  does not parse is the router's 400. A paged operation's record also
  takes `cursor: Option<String>`, a page's `next`, and
  `limit: Option<i64>`, held to the route's limits as the router holds
  the query's (`PageSpec::request`).
- **Result.** It answers `Result<OutputRecord, ProblemRecord>`, a paged
  operation its page record.
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
  browser holds no service credential. Nor has it an end user's call:
  only a service's ([Calling as a service](#calling-as-a-service)).
  An `@allowService` operation keeps its procedure, since its route
  admits an end user too.

The doc of the operation's call, or of its guard, says why, and so does
the build log. The result of an operation without a call gets no record,
and its input no form. A type that only a left-out procedure's arguments
name gets no record either.

### Calling as a service

An operation with a service clause, `@requireService` or `@allowService`
([D37](../../docs/DECISIONS.md)), is called as a service through
`operations::as_service(&caller)`. The caller is the runtime's
`ServiceCaller`, the record the route hands an implementation for a
verified credential, which the app names itself:
`ServiceCaller::in_process(deployable, serves)`, `serves` the APIs it acts
for. Nothing in one process presents or verifies a credential, and the
process's own code is trusted, so the rule makes the call deliberate; it
does not stop an attack.

- **`as_service(&caller).<ns>_<op>(cx, args)`** runs the route's service
  step: 403 `service_forbidden` when the route's `from` lists none of the
  APIs the caller serves. Where the route still asks for an end user, it
  takes the page's caller, from the `PageAuthenticator`, as the end user
  the service forwards (401, 403). A listed caller of an `@allowService`
  operation stands in for the end user; one its `from` does not list goes
  on to the end-user step, as the route sends it on. Then `Args::check`
  and the implementation run, the caller in `ctx.service_caller` as the
  route hands it.
- **`as_service(&caller).can_<ns>_<op>(cx)`** admits the caller alone.
- A `@requireService` operation has **no end user's call**, so a page
  cannot call it for a reader, by accident or not, and the compiler says
  so. Its guard, `can_<ns>_<op>(cx)`, answers 401 `service_unauthorized`,
  as the route refuses a request without a service credential. Its
  argument form has **`submit_as(cx, &caller)`** in place of `submit(cx)`.
  One without an in-process call, a manual one say, keeps that guard
  alone: `as_service` has neither its call nor its guard.
  An `@allowService` operation keeps its end user's call, its procedure
  and its pages, which call it as the end user, never as a service.

An app that hosts a stock API's crate, whose `reserveStock` admits only
`@requireService({ from: [shop-orders] })`, reserves as the orders service
it acts for, forwarding the signed-in reader:

```rust
#[page(POST "/stock/{id}/reserve")]
async fn reserve(cx: &Cx, Form(form): Form<StockReserveStockArgsForm>) -> topcoat::Result<impl View> {
    let orders = ServiceCaller::in_process("shop-app", ["shop-orders"]);
    let form = StockReserveStockArgsForm { id: Some(path_param::<Id>(cx)?.to_string()), ..form };
    let refused = match form.submit_as(cx, &orders).await {
        Ok(_) => return Err(see_other("/stock").into()),
        Err(refused) => refused,
    };
    Ok(view! {
        (refused.status)
        <p>"Not reserved"</p>
    })
}
```

Call one only where the page acts as that service: the call carries the
service's authority, not the reader's. The console and the procedures
never call as a service, and a call as a service applies no traffic
controls, as no in-process call does.

### Route controls

A procedure meets its route's `@rateLimit`, `@bodyLimit` and `@timeout`
as the route's request does, in the route's order, and answers each
refusal as the route does, as a `ProblemRecord`: 429 `too_many_requests`
(with `Retry-After`), 413 `payload_too_large`, 504 `gateway_timeout`.
`<service>(...)` puts a `ProcedureControls` layer on the path of each
such procedure:

- **Rate limit.** The procedure takes the operation's limiter from the
  app's registry, `Setup.rate_limits`, the one the JSON API's route and
  the [pages](#route-controls-of-the-pages) take too
  ([D44](../../docs/DECISIONS.md)). It counts each client by its IP
  address as Topcoat reads it (`client_ip`: the peer's, or the one a
  trusted proxy names), and `<service>(...)` gives the JSON API's routes
  the same address, so a client has one budget per operation through all
  three, and the clients without an address share one. Another process
  that serves the API, such as a stack's server beside the app, keeps a
  registry of its own: a limiter lives in process memory, so replicas
  share no budget.
- **Body limit.** The procedure's request body is read up to the limit
  before its arguments are decoded, and Topcoat's own limit is raised to
  it for that procedure.
- **Timeout.** It covers the arguments' decoding and the call.

A control a procedure could not apply as its route does would leave the
procedure out, with the reason, rather than be skipped; all three apply.
An in-process call from the app's own page applies none of them: the
page is a route of the app, and its controls are the app's to set. The
[generated pages](#route-controls-of-the-pages) apply them to their own
requests.

## Display components

`views` has two components per record, which a page renders as it
renders any component:

- **`<type>_detail(record: <Type>Record, choices)`** renders a record as a
  description list: a `<dt>` label and a `<dd>` value per field, each in a
  `<div data-field="<json key>">`, inside
  `<div class="ss-detail" data-type="<Type>" role="group">`.
- **`<type>_table(rows: Vec<<Type>Record>, choices)`** renders records as
  a `<table class="ss-table">`: a header row of labels, then a row per
  record, each cell `data-field="<json key>"`.

`choices`, optional, is the `Choices` a form takes ([Choices the app
supplies](#choices-the-app-supplies)).

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
| A string, a UUID, a number | its text, or the label the app's `choices` give it |
| An enum | `<data value="on_hold">On hold</data>`: the member's title, else its name in words, which `<enum>_label` gives |
| `Temporal.DateTime` | `<time datetime="2026-10-09T15:30:00+01:00">9 Oct 2026, 14:30 UTC</time>`: in UTC, to the minute |
| `Temporal.Date` | `<time datetime="2026-10-09">9 Oct 2026</time>` |
| `Temporal.Time` | `<time datetime="14:30:15">14:30</time>`: to the minute |
| An amount whose currency is known | `<data value="3998">GBP 39.98</data>` ([Money](#money)) |
| A boolean | "Yes" or "No" |
| An object type | its detail; in a table, its title when its type declares one |
| A list of objects | its type's table |
| Any other list | `<ul class="ss-list">` |
| A map | `<dl class="ss-map">` of its entries |
| A union, or any JSON value | its JSON text in `<pre class="ss-json">` |
| Optional, absent | nothing: an empty `<dd>` or `<td>` |

A time's text is the same for every reader, in English and in UTC, with
no locale or zone of the server's; `datetime` keeps the value as the API
sends it, for a script that shows it in the reader's own zone. Text that
is no date or time shows as it is. `views::date_time_text`, `date_text`
and `time_text` write it for a page's own markup.

An API type does not say which record an id names either, unless the id
refers to one (`RefersTo`), whose labels `references` gives ([What a
value refers to](#what-a-value-refers-to)), so a component shows the label
the app's `choices` give a text or number value, an order line's
product's name for its `productId`, and the value itself when they give
none. A field is named by its path without the rows'
indexes, `lines.productId` for each line of an order's detail, and a
nested record's component gets the choices under its field. A choice
matches a value whose text is the choice's value exactly: an id as the
API sends it, its compact text, which a record holds and `to_string()`
gives.

```rust
let choices = Choices::new().with("lines.productId", catalog.iter().map(|product| (product.id().to_string(), product.name)));
view! { order_view_detail(record: OrderViewRecord::from(order), choices: choices) }
```

The type's `@display` ([D48](../../docs/DECISIONS.md)) shapes both:
`summaryFields` chooses and orders a table's columns (else every field),
`titleField` names a detail (`aria-label`, falling back to `noun`) and
heads each row (`<th scope="row">`), and `plural` captions a table.

Every value is text the view escapes, never markup. The components ship
no CSS and no inline styles: a stylesheet styles them by their `ss-`
classes and `data-field` attributes, and fills an empty value with
`:empty`. A type that nests itself boxes its components' views, as
Topcoat requires of a recursive component.

A table's rows link to pages through `hrefs`, a prop every
`<type>_table` takes, empty by default: its i-th entry, unless it is
empty, links row i, on its title's cell, or on its first cell when the
columns hold no title. A linked cell without text, an absent or blank
title say, links the type's noun in its place (its `@display` noun, else
its name in words), with the row's `id` when the record holds one as
text or a whole number, `Document intro`, as the pages' trail names an
item without a title, so every row can be reached. The [pages](#pages)
fill `hrefs`; a page of the app's own can too:

```rust
let hrefs: Vec<String> = rows.iter().map(|row| format!("/orders/{}", row.id)).collect();
view! { order_view_table(rows: rows, hrefs: hrefs) }
```

A table that a sorted list's console page shows (D44, amended) takes
`sorted`, a `views::Sorted`, default none: the header of each column
that is one of the list's sort fields is a link,
`<a href=… data-ss-sort="placedAt">`, and the column the rows are in
order of has `aria-sort="ascending"` or `"descending"`, which no other
header repeats. `Sorted.by` is that column and its direction, and
`Sorted.links` each sortable column's field, link and query, which a live
list's `Sorted.live`, its shard's query, takes in place of loading the
page. An item's page shows a section's table with none.

## Money

A field that holds an amount in a currency's minor units says which
currency with `@display({ currency })` ([D48](../../docs/DECISIONS.md)):
a code, `"GBP"`, or another field of the same type, `{ field: "currency" }`.
A `Finance.Money` field without it takes its type's one `Finance.Currency`
field, when the type has exactly one.

A display component shows the amount as
`<data value="450">GBP 4.50</data>`: the code, a space, `-` for a
negative amount, the whole part grouped by `,` in threes, and exactly
the currency's minor units of decimals. XAU, which has none, shows the
whole number. A field currency is read from the same record, per row in
a table. A currency that is absent or no active ISO 4217 code shows the
amount as a plain number, and the build log names each amount a
component shows as a number, and why:

```
  - topcoat: OrderView.total shows as a number: it is Finance.Money, and names no currency: @display({ currency }) gives it one
```

`views::money_text(amount, currency)` writes the text, for a page's own
markup. The minor units come from superscalar's Rust core at run time
(`finance_currency::lookup`), through the scalar crate the types crates
link, which the crate's `Cargo.toml` names only when it shows or reads
money. Money takes none of the app's choices.

A form's amount in a literal currency is a decimal control,
`<input type="text" inputmode="decimal">`, labeled `Deposit (GBP)`, with
a pattern from `forms::decimal_pattern(code, signed)`: at most the
currency's decimals, and a `-` only when the field's bounds admit a
negative amount. Its reader, `money_<code>` or `signed_money_<code>`,
takes exactly what the pattern takes, and writes the minor units:
`4.50` is 450. It refuses, at the field, "must be an amount" (surrounding
space included), "must have at most 2 decimal places", "must be a whole
amount" (any `.` in JPY), "must not be negative" and "is too large an
amount". A default shows in major units (`decimal_text(2500, "GBP")` is
`25.00`), and an edit page fills the control from the item the same way
(`held_decimal`), so saving it unchanged keeps the amount. The app's
choices do not make it a select: a choice's value is minor units, as the
API sends it.

An amount whose currency is another field, named or inferred, cannot
know its decimals before that field is read, so it stays
`type="number" step="1"`, with the hint "In minor units of Currency". Its
reader, `minor_amount`, takes digits alone, with an optional `-`, and
refuses `12.00` and `1.2e1` ("must be a whole number of minor units"),
which a number input sends and an integer reader would take as 12.

## Pages, references and search

### A paged list

A `@paginated` operation (D60) takes the page asked for in its `Args`,
`page`, and returns a page, its items and the cursor of the next, none
after the last. Its in-process call takes `PageRequest::first(limit)` or
`PageRequest::after(next, limit)` and returns
`runtime::page::Page<types::X>`; its procedure takes `cursor` and `limit`
and answers its page record ([Records](#records)). Its argument form, which
every paged operation has, one with no other argument too, holds `cursor`
and `limit` as the query sends them and renders neither:

- **`page()`** reads them by the route's rules (`PageSpec::query`): an
  absent cursor is the first page and an absent limit the route's
  default, and a cursor outside the base64url alphabet or a limit out of
  bounds is the router's 400 at its parameter. `parse()` builds `page`
  from it, and `submit(cx)` answers a refused cursor or limit with that
  400, as the JSON API does; a cursor the implementation cannot read is
  its own 400 at `cursor`.
- **`query_pairs()`** is the query the form holds: each argument's values
  in control order, then `limit` when it was sent, never `cursor`, and
  never the console's `back`, which no form holds. A page links its next
  page with it and `cursor` set to the page's `next`, and its first page
  with it alone, so both keep the filter and the limit, and a submitted
  filter, which holds no cursor, starts at the first page.

```rust
#[page("/orders")]
async fn orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let pairs = filter.query_pairs();
    let (status, rows, next, errors) = match filter.submit(cx).await {
        Ok(page) => {
            let next = page.next.map(|next| format!("/orders?{}", query(&pairs, Some(next.as_str()))));
            (StatusCode::OK, page.items.iter().map(OrderViewRecord::from).collect(), next, FormErrors::default())
        }
        Err(refused) => (refused.status, Vec::new(), None, refused.errors),
    };
    Ok(view! {
        (status)
        <form method=(OrderListOrdersArgsForm::METHOD)>
            order_list_orders_args_fields(form: filter, errors: errors)
            <button type="submit">"Filter"</button>
        </form>
        order_view_table(rows: rows)
        if let Some(next) = next {
            <a rel="next" href=(next)>"Next page"</a>
        }
    })
}
```

(`query` percent-encodes the pairs, as the [pages](#what-each-page-does)
do.) A paged list's console page shows its pager after its table: the
first page, the page before, which the history in its query's `back`
gives, and the next, then a form of its page sizes ([What each page
does](#what-each-page-does)).

A sorted operation, one with a `SortBy` argument (D60, amended), reads its
order in `page.sort`. Its in-process call unbinds the cursor from the sort
before the implementation reads it, the default sort filled in, and binds
the page's `next` to the sort after, as the router does, so a cursor of
the JSON API pages the call and one of the call the JSON API; a cursor
returned under another sort is the 400 at `cursor`. Its argument form
holds the sort as it holds the limit, under the argument's name, `sort`,
as the query sends it, and renders no control for it: `page()` reads it
by the route's rules, a sort the route does not offer being its 400, and
`query_pairs()` keeps it before `limit`, so a page's links keep the order.
Its procedure's record takes it beside `cursor` and `limit`
(`PAGE.sorted_request(cursor, limit, sort)`). The console's headers set it
([What each page does](#what-each-page-does)).

### What a value refers to

A field or an argument declared `RefersTo<T, Target>` (D61) holds the
value of an item of `Target`, as an operation of the API lists it: its
options operation, its value field, `id` unless it says another, its
label field, the target's `@display` titleField unless it says another,
and its lookup, the options operation's optional list query argument that
refers to the target by the same value, each derived by the core's
`ir.ResolveReference`. When a form or a record the crate has holds one,
and its options operation has an in-process call, the crate writes
`references`:

- **`<form>_choices(cx, form, choices) -> Choices`**, per form that holds
  a reference (`order_input_form_choices`, `order_list_orders_args_form_choices`),
  gives each such control, by its path (`lines.productId`, an argument by
  its name), the target's items as options, each by its value and named
  by its label. They are the options operation's items from one listing
  a request, the first page of up to its maxLimit when it pages; when
  more follow, a select would hide them, and the control stays as it is.
  A control whose options operation searches is a search picker instead,
  whose pick it gives by its control's name from `form` ([A search
  picker](#a-search-picker)).
- **`<record>_labels(cx, records, choices) -> Choices`**, per record that
  shows a reference at any depth (`order_view_labels`), names each value
  the records hold by its item's label: through the lookup with the
  records' distinct values, in chunks of the lesser of its listMax and,
  when the options operation pages, its maxLimit, at most
  `references::MAX_LOOKUP_CALLS`, 10, calls a target each time it is
  called; without a lookup, a reference by sku say, from the listing. A
  value no item has shows as it is. A target's own record may show a
  reference too, as a product names its supplier: its labels function is
  still `<record>_labels`, and the functions `references` keeps for the
  target are named apart from it.

Both answer under `choices`, the app's, through `Choices::or`: the app's
win where they name a path, and no call is made for it. The listing and
each lookup are kept for the request (`#[memoize]`), so every reference
to a target in a render shares them; nothing is kept across requests,
since labels change and callers differ. A caller the operation refuses
(401, 403), or a call that fails, gets the plain control and the value as
it is; the page does not fail. A reference whose options operation has no
in-process call, a signed webhook's say, is left out, its control and
value as they were, with the reason in the build log: `PlaceOrderLine.productId
refers to ProductView, whose options operation ProductCatalog.listProducts
has no in-process call: ...`.

A field reads its reference through `ir.FieldReference`: its `RefersTo`,
or, on a `@source` type, the reference a relation's key derives (D61,
amended). `OrderLineView.productId`, beside `@source(shop-db.OrderLine)`
whose `product` is a `Relation<Product>`, refers to the one type of the
API sourced from `shop-db.Product` with no `RefersTo`, and is offered and
labeled as a written reference is. A target may be an API dependency's
type (D66), imported by the API, and the build log names it by its
service, `fixture-console-suppliers-api.Supplier`. Its options operation
is one of the API's own when one lists it; else it is the dependency's
own, by the same rule (D61, amended), which the crate calls through the
dependency's crate when one app hosts both ([Several services in one
app](#several-services-in-one-app)). Its functions are named after the
dependency, `fixture_console_suppliers_api_supplier_list_suppliers_options`,
and a page that calls it meets its rate limit, its limiter taken from the
dependency's setup at the request. Otherwise the reference is left out,
and the build log says why: `... whose options operation
fixture-console-suppliers-api's SupplierQueries.listSuppliers has no
in-process call: [extension.topcoat] services does not list
fixture-console-suppliers-api beside fixture-console-api, so no app hosts
both`, or the dependency's server is not Rust, or its crate is not built.

An app's own page takes them as the console's pages do:

```rust
#[page("/orders/new")]
async fn new_order(cx: &Cx) -> topcoat::Result<impl View> {
    let form = PlaceOrderInputForm::new();
    let choices = references::place_order_input_form_choices(cx, &form, Choices::new()).await;
    Ok(view! {
        <form method="post">
            place_order_input_fields(form: form, choices: choices)
            <button type="submit">"Place the order"</button>
        </form>
    })
}

#[page("/orders/{id}")]
async fn show_order(cx: &Cx) -> topcoat::Result<impl View> {
    let args = OrderGetOrderArgs { id: path_param::<Id>(cx)?.clone() };
    let order = OrderViewRecord::from(operations::order_get_order(cx, args).await?);
    let labels = references::order_view_labels(cx, std::slice::from_ref(&order), Choices::new()).await;
    Ok(view! { order_view_detail(record: order, choices: labels) })
}
```

### A search box

A `SearchQuery` argument (D62) is its argument form's first control, an
`<input type="search">` labeled "Search", with its rules' attributes
(`maxlength`), which no choices make a select. The console's filter form
that holds one is `role="search"`, and its links to the next and the
first page carry it. The implementation matches it: `runtime::search::terms`
and `like_pattern` over SQL, `matches` in memory.

### A search picker

A control whose reference's options operation has a `SearchQuery`
argument is a search picker (D61, amended), however many items there are:
a search box, `_find.<control>`, labeled `Search <plural>` and with the
search argument's attributes, a "Find" button, and the control's select,
whose options are the first page of the items the box's text finds, at
the options operation's defaultLimit, or of every item for an empty
text, with a note when more match. The value held is always an option,
named by its label through the lookup, else the listing, as a record's
value is. The control keeps its label, and the select its id and name.

`<form>_choices(cx, form, choices)` gives each picker its `Pick`
(`crate::choices::Pick`) in `Choices`, by its control's name
(`Choices::with_pick`, `Choices::pick`): each distinct text is one
listing, kept for the request, and a request searches at most
`MAX_LOOKUP_CALLS` distinct texts through one options operation, every
picker of every form and target that it lists counted together
(`<operation>_searched`). A picker past them lists the first page and
says to search again. The values the form holds are named through the
lookup in one batch. The form holds every box's text in `finds`
(`Finds::text`), and every picker searches its box's text, so the box
always says what its select lists. A filter's "Find" sends its query,
`_find.productId=anvil`, and the page renders the matches beside the
list. A post's "Find", `_action=find:<control>` ([Rows without
JavaScript](#rows-without-javascript)), renders the form again with every
text searched, its own past the cap, and so does a row button's post or
a refused one: a box that held `bel` when the reader added a line still
lists what `bel` finds. A text the search argument's rules refuse shows
the route's message and no matches. The app's `Choices`
still win: a control they name is a select of theirs, with no picker and
no call. A caller the options operation refuses gets the plain control
and the value. A query list argument, whose controls share its name,
stays a select of the options.

Each picker is a shard of its source, `references::<target>_pick`, at
`/_superschematic/<service>/_pick/<target>`, taking the control
(`PickControl`) and its pick. Without Topcoat's runtime it renders in
place and "Find" submits the form. With it, the box's `input` event sets
the shard's text, and the runtime renders the picker again with what the
text finds, through `<target>_target_picks`, the value the select holds
kept, while Topcoat keeps the focused box's text. "Find" counts its
presses in a signal the shard reads tracked, so each press renders it
again and searches the box's text, the same text too: a picker that a
post rendered past the cap searches when the reader presses it. The shard calls the
options operation in-process as the page's caller, and meets its rate
limit and timeout through a layer on its path (`references::route_controls`),
whose limiter is the operation's one in the app's registry.

## Pages

`pages: { enabled: true }` in `outputs.topcoat` (or `pages_prefix` in
`[extension.topcoat]`) adds `pages`: a page for every resource the API's
operations describe, under a prefix, `/console` unless `prefix` says
otherwise. A prefix starts with `/`, does not end with one, holds only
segments of letters, digits and `-._~`, and is not under `/api` or
`/_superschematic`, which the crate serves itself. The pages need the
records, the forms and the display components; turning one of those off
with the pages on fails the build.

### Resources

The resources come from the operations a page can call (the ones with an
in-process call, and not `@requireService`), by REST path and result:

| Operation | Page |
| --- | --- |
| `GET C` returning `T[]` | C's list, `<prefix>/C`, its query arguments the filter form |
| `GET C/{p}` returning `T` | an item's page, `<prefix>/C/{p}` |
| `POST C` with an input or arguments | the new page, `<prefix>/C/new`, and its POST |
| `PUT` or `PATCH C/{p}` with an input | the edit page, `<prefix>/C/{p}/edit`, and its POST |
| `DELETE C/{p}` | a link to `<prefix>/C/{p}/delete`, which confirms, and its POST |
| `POST`, `PUT` or `PATCH C/{p}/<verb>`, where `C/{p}/<verb>` names no collection | the action's form on the item's page, posting to `<prefix>/C/{p}/<verb>` |
| `C/{p}/D` | D nested under C's item: its pages under `<prefix>/C/{p}/D`, and its list and new link a section of C's item page when C has one |

A list or a detail makes C a resource; the edit page, the delete and the
actions need the detail, whose page holds them. Every other operation is
left out, and the build log says why: `no page for viewer.me: a GET of
auth/me, a path that ends in a word, is a list only when it returns a list
of records`. Among them are a `PUT`, `PATCH` or `DELETE` of a
collection's own path, `widgets` or `widgets/{id}/parts`, which would
change all of it, and a `DELETE` of `C/{p}/<verb>`, since an action's
form posts from the item's page in one click and a delete asks first.

A resource is named by its type's `@display` (`noun`, `plural`,
`createLabel`, `titleField`) where it declares them. Else the collection
is titled by its path's last word as the path gives it, `shelves` is
Shelves, and an item by its type's name in words, `Shelf` is a Shelf,
`OrderView` an Order view, as a table names an untitled row: no label
guesses a singular from a plural, which English rules get wrong (shelves
is no shelve). The new page is then "New shelf". A collection whose path
extends another's, `documents/drafts` beside `documents`, is titled by its
own words after the other's, Drafts, never by its type's `plural`. A
collection without pages of its own, only a step to one under its items,
names an item in the trail by its id alone. A list's rows link to their
items' pages when the list returns the detail's type and the detail's
path parameter names one of its fields (`{id}` and `id`), or refers to
the detail's own type (`RefersTo`), by the reference's value field
(`products/{productId}` and `id`).

A collection whose pages would be served at another's has none, and the
build log says why: a list at `projects/new`, beside the projects' new
page, or a collection named `edit` or `delete` under an item that has an
edit or a delete page, `projects/{id}/edit`. Topcoat's router refuses a
page registered twice, so the console's own pages keep their paths:
`no page for project.listNewProjects: its collection, projects/new, has no
pages, since the list of projects/new would be served at GET projects/new,
the new page of projects`.

### What each page does

Every page calls its operation through `operations`, so the route's
rules hold, and takes its input through the [forms](#forms). Its request
meets the route's traffic controls too ([Route controls of the
pages](#route-controls-of-the-pages)).

- **A list** checks the guard, renders the filter form (a GET, read from
  the query), its search box first, and the result as `<type>_table` with
  each row linked, and a link to the new page when the caller may create
  one. A paged list renders the page's items, then
  `<nav class="ss-pager">`, each link with the filter and the limit:
  "First page" (`rel="first"`) while a cursor was sent, "Previous page"
  (`rel="prev"`) while the page before is known, and "Next page"
  (`rel="next"`) while one follows. On the second page Previous is First,
  and both render, so each page offers its links in the same places. A
  refused cursor offers the first page alone. A refused limit is kept by
  no link and no form, so the 400's First link, while a cursor was sent,
  and its filter start again at the route's default. A paged section
  on an item's page shows the first page and a link to the list's next,
  with no history and no size form. A paged list without a filter renders
  no filter form, and a refusal of its page on its own, with its status.
- **Previous** comes from the query's `back` (D44, amended): the cursors
  of the pages before the current one, oldest first, from the second
  page on, joined by `.`, `c2.c3` on the fourth page. Next carries the
  current `back` with the current cursor after it; Previous carries the
  last cursor of `back` and the rest, or no cursor on the second page.
  `back` holds at most 4096 characters (`MAX_BACK`): while the next one
  would be longer, its oldest cursors are dropped, and a leading `~` says
  so, so its oldest entry's page before is unknown and has no Previous. A
  `back` that breaks the rule (longer than the bound, an entry that is no
  cursor by D60's rule, a `~` past the first entry), or one sent without
  a cursor, reads as unknown: no Previous, never a 400. No operation reads
  `back`, and the argument form ignores it. An operation that declares an
  argument named `back` keeps no history and links no page before, and
  the build log says why: `no previous page for orders: order.listOrders
  declares an argument named back, the query parameter the console keeps
  the earlier pages' cursors in`.
- **The size form** follows the links, a GET form, `<form
  class="ss-page-size">`: the filter's pairs as hidden inputs, without
  `limit`, and a select named `limit`, labeled "Per page", of the route's
  `defaultLimit` and `maxLimit`, each of 10, 20, 50, 100, 200, 500 and 1000
  below `maxLimit`, and the limit sent when the route admits it, in order,
  the limit sent selected, else the default: 2 and 5 for a route of
  `{ defaultLimit: 2, maxLimit: 5 }`, 10, 20, 50 and 100 for the defaults.
  A route of one size has no form. It sends no cursor and no `back`, so a
  new size starts at the first page; the filter form holds the limit sent,
  when the route admits it, as a hidden input, so a filter keeps the size,
  and it too starts at the first page.
- **The headers** of a sorted list's table (D44, amended) each link a
  sort field's column: the column the rows are in order of, the sort sent
  or the route's defaultSort, has `aria-sort`, and its link reverses the
  order; another sortable column's link sorts by it ascending. A link's
  query is the list's with the new sort, before the limit, and no cursor
  and no `back`, so a new order starts at the first page with no history.
  The filter holds the sort sent, when the route admits it, as a hidden
  input, and the pager's links and the size form keep it; a sort the
  route refuses is its 400, as the form's message, and no link keeps it.
  A section on an item's page shows its first page in the defaultSort,
  with no links. A sort field the table shows no column of is sorted only
  by the query, and the build log says so: `topcoat: orders sorts by
  totalCents, which its table shows no column of`.
- **An item's page** renders `<type>_detail`, headed by its title, links
  to its edit and delete pages, a form per action and a section per
  resource under it, each only when the caller may.
- **The new and edit pages** render their form, the edit form filled
  from the item: each field the update's input and the item's type share
  by JSON name and kind; any other starts as the form's `new()`.
- **The delete page** shows the item and the form that deletes it, so a
  GET never deletes and no script confirms.

Every form a page renders takes its references' options and its search
pickers' picks, and a list's rows and an item's detail their records'
labels ([What a value refers to](#what-a-value-refers-to)), under the
app's choices; an action's form takes its own beside the detail's
labels. A post's "Find" renders its page's form again, 200, without the
operation ([A search picker](#a-search-picker)).

A route's values come from the page's path, set on the form over its
hidden inputs, which a reader can change, and decoded as an argument form
decodes them; one that does not decode is no page, 404.

A successful post goes on with 303: to the new item after a create, to
the item after an edit or an action, to the list after a delete. A post
is an argument form's `submit`, or an input form's `parse` and its
operation, and a refusal is its [`Refusal`](#argument-forms): the form
renders again, each error at its control, with the refusal's status, 422
for what `parse` refuses and the route's for what the operation refuses,
a conflict's (409) message the form's own. A row button renders the form
again with 200. A refused caller keeps the route's status on a page of
its own: 401 with a link to sign in, 403, and 404 for an item that is not
there.

### Layout, nav and index

A `#[layout]` at the prefix puts each page in `<main class="ss-main">`,
after `<nav class="ss-nav">`, a link to each resource no item holds whose
list the caller may use (`can_<operation>`), the current one
`aria-current`, and `<nav class="ss-trail">`, the trail from the index to
the page: each list, each item by its title (its type's `titleField`),
else by the label of what its route's value refers to, else by its noun
and id, then the page. An item's step is named by a reference (D44,
amended) when every operation whose route holds the step's parameter at
its place and declares `RefersTo` refers to one target by one value:
the item's detail, edit, delete and actions, and every operation of each
collection under it, matched by the parameter's place, since operations
name it apart. The label comes through `references::<target>_target_label`,
the target's lookup with that one value or its listing, so a supplier's
products at `suppliers/{supplierId}/products`, whose `supplierId` refers
to `Supplier`, name the supplier though no operation reads one. A value
no item has, a blank label, or a call that is refused or fails leaves
the noun and id. When the references disagree the step names nothing,
and the build log says which refers to what: `the trail names
products/{productId} by its id: review.listReviews(productId) refers to
ProductView by id, and stock.getStock(productId) to StockView by id`.
Where a collection's path extends another's, `widgets/archived` beside
`widgets/{id}`, the longer is its own
pages' collection, current in the nav and the trail, never an item named
`archived`; under an item, a collection is read before the item's
actions. The item's detail, which the
item's page calls too, is called once per request (`#[memoize]`), and so
is the nav's list of what the caller may list, which the index shares. A
request asks the `PageAuthenticator` for its caller once
(`#[memoize]`), however many guards its page, its layout and its forms'
calls ask: the nav's guards are permission checks of one caller. The
index at the prefix lists the same resources. The markup is semantic HTML
with `ss-` classes and ships no styles.

A link of the nav and the index starts with its resource's icon when the
list's element type, else the detail's, declares one with
`@display({ icon })` ([D48](../../docs/DECISIONS.md)): an empty span the
app's stylesheet draws, before the label.

```html
<li><a href="/console/orders"><span class="ss-icon" data-icon="receipt" aria-hidden="true"></span>Orders</a></li>
```

The crate ships no icon set: the name is the app's to draw, with a mask
per name, say.

```css
.ss-icon { display: inline-block; width: 1em; height: 1em; background: currentColor;
           mask: center / contain no-repeat; }
.ss-icon[data-icon="receipt"] { mask-image: url(/icons/receipt.svg); }
```

A link whose type declares no icon has no span, and a crate whose
resources declare none renders the nav as it did before icons.

### Mounting them

The pages, the layout and the index are `#[page]`s and a `#[layout]`,
which the app's `.discover()` registers, as it registers the procedures:

```rust
let choices = Choices::new().with("lines.productId", catalog.iter().map(|product| (product.id().to_string(), product.name)));
Router::builder()
    .discover()
    .app_context(pages::Settings::new().title("Shop console").sign_in("/sign-in").choices(choices))
    .shop_orders(implementations, page_authenticator)
    .build()
```

`pages::Settings`, which the app puts in its app context, names the
site's title (the service's name in words without it), its sign-in page
(a 401 links to it with the page to come back to as `next`, the item's
page for an action; without it a 401 has no link), and the
[choices](#choices-the-app-supplies) every form and display component of
the pages takes, by each field's path: `lines.productId` makes each
line's product a select on the new-order page and its name in an order's
lines. `choices` are the app's for every resource, so a path names its
field in each form and component that holds one. `choices_for` scopes
choices to one resource's pages, its list, its item's page, its new, edit
and delete pages and its section on its parent's page, named by its path
below the prefix as `pages::RESOURCES` lists it, its parameters as its
pages name them. A resource's choice for a field wins over the app's for
that field on its pages, whose other fields keep the app's; a name no
resource has panics, as a typo would. A field that refers to an item
(`RefersTo`) needs none: its references give its options and labels, and
the app's, where they name it, win:

```rust
let settings = pages::Settings::new()
    .choices(Choices::new().with("ownerId", members))
    .choices_for("projects/{project_id}/tasks", Choices::new().with("ownerId", assignees));
```

The page functions and their path parameters are public, so an app's own
page can link to one with
`href!(pages::projects_item_page, pages::ProjectId(id))`.

An app that mounts two services' pages gives each a prefix of its own,
as the [table](#the-config) requires: each crate registers its index and
its layout at its prefix, and Topcoat's router refuses a page registered
twice.

### Route controls of the pages

A page's request meets the `@rateLimit`, `@bodyLimit` and `@timeout` of
the operation it is for, in the route's order, as the route's request
does, and of the options operations its references call, so the console
is no unlimited twin of a route ([D44](../../docs/DECISIONS.md)). `<service>(...)` adds one layer at the
prefix, `PageControls`, which finds the page by its method and the path
it is registered at:

- a post meets the controls of the operation it submits: the new page's
  create, the edit page's update, the delete page's delete, an action's
  operation;
- a GET page meets those of the operation it shows: a list its list's,
  an item's page, its edit page and its delete page the detail's. The
  index calls no operation, and meets none. A GET page carries no body
  for a body limit to refuse;
- a [live list](#live-lists)'s shard meets its list's, and a [search
  picker](#a-search-picker)'s shard its options operation's rate limit and
  timeout, through a layer on its path, `references::route_controls`;
- a page whose forms or records hold a
  [reference](#what-a-value-refers-to) meets, besides, the rate limit and
  the timeout of each options operation they call, listing or looking up
  its target: the new page its form's, a list's page and its shard its
  filter's and its rows', an item's page its detail's, its actions' forms'
  and its sections' rows', the edit page its form's, the delete page its
  form's and the item's, and a post those of the page it renders again.
  Not the lists its sections call, which keep to their own pages;
- a page whose trail names a step by a reference meets the rate limit
  and the timeout of that reference's options operation: the pages under
  the step's item, and the item's own page, edit, delete and actions. A
  shard renders no trail.

The rate limit is the operation's limiter in the app's registry,
`Setup.rate_limits`, which every page and shard that calls the operation
takes from, and so do its JSON API route and its procedure; an API
dependency's options operation's is its own crate's, which the page takes
from that crate's `Setup` at the request, none when the app does not set
it up ([Several services in one app](#several-services-in-one-app)): a client has
one budget per operation through all of them, counted by its IP address
as Topcoat reads it (`client_ip`). A reference's options operation is
counted with its route too, so a client who reloads a page holding a
reference spends that listing's API budget, and a busy console can leave
the client none for the JSON API's listing. A page that meets several
operations' controls is counted by each limiter, its own operation's
first, and the tightest timeout covers it; the body limit is its own
operation's, since no other call carries the body. The body is read up
to its limit before the page decodes its form, and the timeout covers
the page's render, its calls included. A refusal is the route's status,
429 with `Retry-After`, 413 or 504, and the pages' refusal section, its
heading and the route's message, which no layout frames, since the page
did not run:

```html
<section class="ss-refusal" data-status="429"><h1>Too many requests</h1><p>Too Many Requests</p></section>
```

Besides its references', a page meets the controls of its own operation
only: the sections an item's page shows, and the item a refused delete
renders again, are calls of a page already admitted. A reference's call
is not: it lists up to a page's maxLimit of items, or looks up as many,
on any page that holds one, so a client repeating the page would list
them past the options operation's limits. An in-process call from the
app's own page applies none of them, as before.

### Live lists

`pages: { enabled: true, live: true }` renders each list with a filter,
or with a page, through a Topcoat shard, `<prefix>/_live/<resource>`, `/console/_live/orders`,
a path that does not change with the build. Without Topcoat's runtime in
the browser the page is as it was: the filter is a GET form, and its
submit loads the page for the query. An app that loads the runtime gets
the filter's results in place: the form's submit handler stops the
browser's own, writes the form's query, as the browser would have sent
it, to a hidden input whose input event sets the shard's signal, puts the
query in the address bar, so a reload or a link keeps the filter, and the
runtime renders the shard again with it, the heading, the filter with its
errors and the table.

The shard reads the query as the page reads its own, through the list's
argument form, and calls the list through `operations`: a shard's
request meets no page's guard or layout, so the call admits the caller
and checks the filter as the route does, and answers a refusal with the
route's status (401, 403, 404, 422), as the page does. A nested list's
shard takes its parent's id as an argument, as the page takes it from
its route, and its query's hidden path value is ignored, as the page's
is. Topcoat's runtime applies only a 200: a refused re-render leaves the
table as it was, and the browser's console names the failed request,
while the page itself, reloaded, shows the refusal. The form's controls
carry the rules a browser checks before the submit runs.

A paged list's links set the shard's query too, so a page renders in
place and its query goes in the address bar; without the runtime they
are links to the page. The shard reads `back` from its query as the page
reads it from the request's, so Previous, First and Next are the page's
links, and its size form's submit runs the filter's script over its own
form (`data-ss-live-size`), through its own hidden input, which sets the
same signal. A sorted list's header links set it as the pager's do
(D44, amended), so the shard renders the new order, its headers'
`aria-sort` following it. A list without a filter or a page has nothing
to update, and stays a component. A search picker is a shard of its own
whatever `live` says ([A search picker](#a-search-picker)). An action's form, a post that changes the item,
keeps its POST-redirect-GET
without a reload of its own: a shard renders and a form posts, so posting
through one would run the operation inside a render, and through a
procedure would need the form's fields as a record the browser builds.

An app loads the runtime once, for all its pages. The console's layout
renders no `<html>`, so the app's own root layout gives the document its
head and the runtime's script, around the console's layout, and the
router serves the asset bundle (`topcoat asset bundle` builds it beside
the binary) and the runtime's layer:

```rust
#[layout("/")]
async fn document(slot: Slot<'_>) -> topcoat::Result<impl View> {
    Ok(view! {
        <!DOCTYPE html>
        <html>
            <head>topcoat::runtime::script()</head>
            <body>(slot)</body>
        </html>
    })
}

Router::builder()
    .discover()
    .app_context(settings)
    .shop_orders(implementations, page_authenticator)
    .assets(AssetBundle::load()?)
    .runtime()
    .build()
```

An app that does not load it leaves `live` off, so it registers no shard
it cannot use.

### Engine pages

When the crate mounts an engine ([The engine](#the-engine)), its pages
are on and a route of the API authenticates a caller, `engine_pages`
serves the engine's types under `<prefix>/engine` (D44, amended):

| Page | Path |
| --- | --- |
| The types the caller may read (the engine's `capabilities`) | `<prefix>/engine` |
| A type's instances, a page at a time, and its schema-scope operations | `<prefix>/engine/{schema}` |
| An instance: its own fields, a section per behavior, then the operations no section shows | `<prefix>/engine/{schema}/{id}` |
| A read-only operation's GET, which renders its result on the page, and a write's POST, then 303 | `.../operations/{operation}`, the type's or the instance's |

Every page calls the engine in-process on the blocking pool, as the
page's caller: the principal the `PageAuthenticator` gives, mapped as
`engine::routes` maps a request's (`Principal::new(subject,
permissions).with_claims(claims)`), so the engine's policy decides what a
page reads and does. No page acts as the system principal: no caller is
401. The API's `@rateLimit` and `@timeout` do not apply. Every POST
passes the console's own cross-origin check before the engine is
called, trusting no other origin, not even the identity config's
`trustedOrigins`, so a post another site sent is 403 `cross_origin`
whatever the `PageAuthenticator` and the origin policy let through. What a page reads renders on the
server; no procedure hands it to the browser.

A page reads the type's describe document. Its `display` names the type
and gives a list's columns: the title field, linked, else the id, then
the summary fields, own or qualified (`Stock.available`). A list pages by
the engine's cursor, with `back` and a size form of 10, 50 and 200 as
the console's lists have ([A paged list](#a-paged-list)). An instance's
own amount shows as money ([Money](#money)) in the currency its field's
`@display` names, else its type's one `Finance.Currency` field's, which
the describe document's `fields` give with each field's scalar in its
`instance` schema. The nav and
the index gain Engine when the caller may read a type, and the trail
names an instance by its title field, else its noun and id.

An operation's form is built at run time from its `params` JSON Schema:
a string a text input, an integer or a number a number input, each with
its bounds, an enum or a boolean a select, and anything else a JSON
textarea, each control named by the bare parameter. A form that writes
an instance sends a hidden `expectedSeq`, the instance's seq when its
page was read. An operation is offered as the engine's tool manifest
offers it to the caller, by each tool's `mcp.hiddenBy`: one the type
lacks, or the deployment hides (`deployment`, from
`outputs.engine.tools.hidden`), is 404, one the policy refuses
(`policy`) or that writes in an archived namespace (`archived`) is 403
with the engine's reason, and one the engine hides from MCP alone, for a
handle MCP would refuse (`handle`), is offered. The engine asks the
policy of that one too, so a caller it refuses has no form. A hidden
tool whose causes the console does not know, or that lists none, is
refused: the console fails closed.

| The engine refuses | The page answers |
| --- | --- |
| A parameter (`OperationParams`) | 422, each issue at its control |
| A behavior's veto | 409, `<code>: <reason>` as the form's message |
| A stale `expectedSeq` | 409, the form as sent |
| A missing instance or operation | 404, on its own page |
| Anything else it refuses | its status and message |
| An error that is no refusal | 500, "The engine failed to answer" |

`engine_panels::panel(cx, behavior, describe, record)` renders the
section of a behavior the crate knows: Workflow, Approvals and Branches
(`core_shows` names the operations each shows), and Stock, Calendar,
Holds and Payments (`commerce_shows`). A panel shows only the operations
the page offers the caller, and its amounts through `money_text`. Any
other behavior gets the generic panel,
`<section class="ss-panel" data-behavior="X">`: its fields as the read
returns them, and its operations as forms. An operation a panel does not
show is a generic form under the panels, so each one the caller is
offered has a form once.

- **Workflow**: the state by its display label, a button for each move
  the display labels out of it, and a "Move to" select of the others, a
  move whose permission the caller lacks left out.
- **Approvals**: each gate's sign-offs against its count, who signed, and
  the caller's sign-off or withdrawal, when their permissions cover the
  gate's and the gate does not exclude its creator. Entering the gated
  state consumes its sign-offs, so a gate the instance entered or passed
  says so: "Met: it entered Approved with its 2 sign-offs."
- **Branches**: the refs, primary first (`?ref=`), a ref's commits 50 at
  a time (`?skip=`), a diff of two of them (`?from=&to=`, at most 200
  rows), the release log, and the writes as `engine_pages::operation_form`
  with the refs and versions filled in: branch, save, commit, seal,
  rebase, revert, merge and discard. A tagged commit's release is a
  button in its row.
- **Stock**: on hand, held, available and low, the receive, adjust and
  count forms, each with the page's `expectedSeq`,
  and the movements 20 at a time (`?movements=`).
- **Calendar**: the time zone, the capacity, the hours as a week, the
  closures, and the bookings between `bookingsFrom` and `bookingsTo` (UTC,
  both days, a week from today by default), 20 at a time.
- **Holds**: the state, the items with their unit prices, and the total,
  "Mixed currencies" when it is null and "Nothing priced" when no item
  has a price.
- **Payments**: due, authorized, captured, refunded, outstanding and
  overpaid, in its currency, the state and the attempts. A refund
  instance links to what it refunds; an instance another type refunds
  through Approvals links to that type; any other holds `refundPayment`'s
  form, in minor units, whose idempotency key is made as the form
  renders.

`pages.engine: false` (`pages_engine = false`) turns the engine pages
off. The build log says why a crate whose pages and engine are on has
none:

```
  - topcoat: no engine pages at /console/engine: no route of the API authenticates a caller, and a page calls the engine only as its caller, whom the engine's policy admits
  - topcoat: no engine pages at /engine/engine: the path is under /engine, where the crate mounts the engine's own routes
  - topcoat: no engine pages at /console/engine: the API's own collection engine is served there
```

The acme shop's console pages `shop-db`'s `Inventory`, `CollectionPoint`,
`Checkout` and `Refund` at `/console/engine`; its
`topcoat/tests/back_office.rs` receives stock, reads a pickup point's
week, shows a checkout's holds and payment as money, and signs a refund
off twice before it moves to approved.

### Sign-in and admin pages

When the API authenticates with the identity runtime (D50) and its
pages are on, `identity_pages` serves, under the prefix:

| Page | Path |
| --- | --- |
| Sign in (GET, POST), then 303 to `next` | `<prefix>/sign-in` |
| Sign out (POST), clearing the cookie | `<prefix>/sign-out` |
| Sign up (GET, POST), with `pages.identity.signUp` or `@userSessions({ register: true })` | `<prefix>/sign-up` |
| The caller's login, name and roles, and the change-password form | `<prefix>/account` |
| The users, a new user, and a user's page | `<prefix>/users`, `<prefix>/users/new`, `<prefix>/users/{id}` |
| A user's posts | `<prefix>/users/{id}/{grant,revoke,disable,enable,password}` |
| The roles, a new role, a role's edit page and its delete | `<prefix>/roles`, `<prefix>/roles/new`, `<prefix>/roles/{id}`, `<prefix>/roles/{id}/delete` |

Each calls the identity runtime's `Service` in-process with the caller as
its `IdentityPrincipal`, so `identity.users.*` and `identity.roles.*`
decide, and no one grants a permission, or administers a user holding
one, they do not hold. Sign-in runs `check_cookie_login`, then `login`
with a cookie session, and sets the runtime's session cookie. `next` is
kept only when it is a path of the app: it starts with `/`, not `//`,
and holds no `\` or control character. A form renders again with each
error at its control, 422 for what the page refuses and the service's
status for what it refuses; a 401, a 404 and a 403 without a list of
permissions render on their own page. A password control never renders
what was sent.

Signing in, signing up and changing a password take a token from a rate
limiter first, counting the client by `client_key`: the API's route's
limiter when the API serves `login`, `register` or `changePassword`, so
the page and the route share one budget, else the page's own at the
route's rate, 10, 5 and 10 a minute. Past it the form answers 429 with
`Retry-After` and "Too many attempts: try again in N seconds".

The nav shows Users to a caller who holds `identity.users.read` and Roles
to one who holds `identity.roles.read` (`Service::permits`), then
Account and a Sign out button (`NavLink.post`), and a signed-out caller
Sign in, and Sign up when it is served, as nav-only links
(`NavLink.nav_only`), since the index has its own sign-in link.

`pages::Settings::sign_in` names the app's own sign-in page, which every
link to sign in then uses; without it they use `identity_pages::SIGN_IN`.
The acme shop names its `/sign-in`, so `/console/sign-in` is routed but
linked from nowhere, and its staff sign out at `/console/sign-out`.
`pages.identity: false` (`pages_identity = false`) turns the pages off.
The build log says why a crate has no sign-up, or none of the pages:

```
  - topcoat: no sign-up page under /console: neither pages.identity.signUp nor the API's @userSessions({ register: true }) opens registration
  - topcoat: no sign-in, account, users or roles pages under /console: the pages of users are at /console/users, where the identity pages would be
```

The identity store lists no user's sessions and ends no one session, so
no page does; changing a password ends every other session.
