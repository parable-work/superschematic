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
- reads and renders a form per input type an operation a page calls
  takes, nested objects and lists included, by the input type's rules,
  and per operation a page submits with arguments, path, query and body,
  by the router's rules, a GET's a filter read from the query
  ([Forms](#forms)).
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

Records, forms, procedures and views are on by default. `forms: false`
leaves out both kinds of form; `procedures: false` and `views: false` each
leave out their module;
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
name, beside its input's fields.

### Input forms

- **`<Input>Form`** holds a value as `Option<String>`, a nested object as
  its type's own `<Type>Form`, a list of objects as a `Vec` of them, a list
  of values as `Vec<Option<String>>` and a list of an enum's members as
  `Vec<String>`. `new()` is a new form: each field's `@default`, a
  required nested object's new form, and each list's first rows (its
  `listMin`, or one when the input requires the list). `Default` is the
  empty form.
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
        <form method="post">signup_input_fields(form: form, errors: errors)</form>
    })
}
```

### Argument forms

- **`<Ns><Op>ArgsForm`** holds each argument as an `Option<String>`, a
  list's every value as a `Vec<String>`, and the input's form as `input`
  when the operation takes one. `METHOD` is `"get"` for a filter and
  `"post"` for any other. `new(<path arguments>)` is the form of one
  resource, its path arguments hidden inputs the page fills (the order's
  id, say), each argument's declared default, and the input's new form.
- **`parse()`** writes each argument as the JSON a request carries,
  decodes it into the operation's `Args` struct, parses the input by its
  form, and runs `Args::check`, so a value the JSON API would refuse with
  400 is that argument's error. A blank optional argument is absent and
  one with a declared default reads it; `check` refuses the first
  argument that breaks a rule. **`submit(cx)`** parses the form and makes
  the in-process call (`operations::<ns>_<op>`), its refusal the form's
  errors.
- **`<ns>_<op>_args_fields(form, errors, choices)`** renders each argument
  with the control an input field of its type gets, labeled by its name in
  words, its description a hint (`aria-describedby`), then the input's
  fields. A list of an enum is a group of checkboxes; any other list an
  input per value sent and a blank one for another; a path argument a
  hidden input.
- A **filter** (a GET's form) reads the query: `Form<T>` reads it on GET,
  and `from_query(cx)` outside an extractor, so the controls show the
  filters in effect. An optional boolean in a filter is absent when its
  checkbox is not checked, so it filters nothing; elsewhere an unchecked
  box is false.

A page whose route holds a path argument fills it from the route before
`submit`, rather than trusting the hidden input, which a reader can change.
`submit` turns every refusal into the form's errors, a 401 or 403 too; a
page that keeps the route's status calls `parse()` and the operation
itself, as acme-shop's `/orders` does.

```rust
path_param!(id: types::IdentityUUID, error = not_found);

#[page(POST "/orders/{id}/cancel")]
async fn cancel(cx: &Cx, Form(form): Form<OrderCancelOrderArgsForm>) -> topcoat::Result<impl View> {
    let form = OrderCancelOrderArgsForm { id: Some(path_param::<Id>(cx)?.to_string()), ..form };
    let errors = match form.submit(cx).await {
        Ok(_) => return Err(see_other("/orders").into()),
        Err(errors) => errors,
    };
    Ok(view! {
        (StatusCode::UNPROCESSABLE_ENTITY)
        <form method="post">order_cancel_order_args_fields(form: form, errors: errors)</form>
    })
}

#[page("/orders")]
async fn orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let (orders, errors) = match filter.submit(cx).await {
        Ok(orders) => (orders, FormErrors::default()),
        Err(errors) => (Vec::new(), errors),
    };
    let rows: Vec<OrderViewRecord> = orders.iter().map(OrderViewRecord::from).collect();
    Ok(view! {
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

### Controls

A control is typed by its field's or argument's type, and carries the
attributes its rules give it:

- email, url, tel, number (`step="1"` for an integer) or checkbox, and a
  `<select>` for an enum;
- `type="date"` for `Temporal.Date` and `type="time"` for
  `Temporal.Time`; `type="datetime-local"` for `Temporal.DateTime`, which
  carries no offset, so the form reads it as UTC
  (`2026-10-09T14:30` is `2026-10-09T14:30:00Z`) and its label ends in
  "(UTC)";
- `type="password"` for a `@secret` field, never rendered with its value:
  a form holds what a reader types, though a record leaves the field out;
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

`FormErrors` holds each control's messages by its name. `parse`'s errors,
and an operation's refusal through `FormErrors::from_api` (its `errors`,
nested for a nested object's fields, and a refused parameter the router's
400 names in its details), are read by path, so each message renders at
its control: a quantity over 99 on the second line is shown on
`lines[1].quantity`. `errors.of(name)` is a control's messages and
`errors.under(name)` a JSON value's or a list's with the place of each. A
message no rendered control shows, an `@uiHidden` field's or a path
argument's, renders with the form's own, after its name.

### Rows without JavaScript

A form whose input holds a list as rows has submit buttons named
`_action`: `add:lines` after a list's rows, unless it holds its
`listMax`, and `remove:lines[1]` in each row, unless the list holds no
more than its `listMin`. They carry `formnovalidate`, so a row can be
added before the others are valid, and the component renders a hidden
submit button ahead of them, which Enter presses. The form holds the
button pressed in `row_action`. `apply_action()`, on the input form and
on an argument form that embeds it, applies it, numbering the rows after
a removed one again with their values as sent, and is true when a row
button submitted the form: the page renders the form again, 200, without
calling the operation.

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
        <form method="post">booking_input_fields(form: form, errors: errors)</form>
    })
}
```

### Choices the app supplies

An input type does not say which values an id may take: a line's
`productId` is a UUID, not a product. The page says, with `Choices`, which
either kind of component takes: a text or number control it names renders
as a `<select>` of the options, after a blank one, and the value held
stays an option when no choice is it. A field is named by its path without
the rows' indexes, for every row, or by its control's name, for one row,
which wins:

```rust
let choices = Choices::new()
    .with("rooms.roomId", rooms.iter().map(|room| (room.id.clone(), room.name.clone())))
    .with("rooms[1].roomId", [(attic_id, "Attic")]);
view! { booking_input_fields(form: form, choices: choices) }
```

### What has no form

An input type a dependency declares gets no form, nor does an operation
whose input has none or one with an argument named as one of its input's
fields, and the build log says why. A form submits through the
operation's in-process call, so an operation without one, a webhook's or
one the service mounts itself, gets none
([what has no procedure](#what-has-no-procedure)). `forms: false` in
`outputs.topcoat` leaves out both kinds.

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
