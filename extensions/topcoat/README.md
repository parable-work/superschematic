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
the views, which are built on records. Pages are off by default:
`pages: { enabled: true, prefix: "/console" }` turns them on, and they
need the records, the forms and the views ([Pages](#pages)); `live: true`
beside them renders each list with a filter through a shard ([Live
lists](#live-lists)).

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
Rust. `outputs.topcoat` in its config, when present, wins. The table's
`pages_prefix` also turns on the listed service's pages, under it:

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
turns on live, as `pages.live` would ([Live lists](#live-lists)); without
a `pages_prefix` the table refuses it.

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
else by its name in words: `OnHold` is "On hold".

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
        <form method="post">signup_input_fields(form: form, errors: errors)</form>
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
        <form method="post">order_cancel_order_args_fields(form: form, errors: refused.errors)</form>
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
`formnovalidate`, so a row can be added before the others are valid, and
the component renders a hidden submit button ahead of them, which Enter
presses. The form holds the button pressed in `row_action`.
`apply_action()`, on the input form and on an argument form that embeds
it, applies it, numbering the rows after a removed one again with their
values as sent, and is true when a row button submitted the form: the
page renders the form again, 200, without calling the operation.

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
`row_action` or the JSON name `_action`, fails the build. A form submits
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

- **`<form>_choices(cx, choices) -> Choices`**, per form that holds a
  reference (`order_input_form_choices`, `order_list_orders_args_form_choices`),
  gives each such control, by its path (`lines.productId`, an argument by
  its name), the target's items as options, each by its value and named
  by its label. They are the options operation's items from one listing
  a request, the first page of up to its maxLimit when it pages; when
  more follow, a select would hide them, and the control stays as it is.
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
type (D66), imported by the API: its options operation is still one of
the API's own, and the build log names it by its service,
`fixture-console-suppliers-api.Supplier`.

An app's own page takes them as the console's pages do:

```rust
#[page("/orders/new")]
async fn new_order(cx: &Cx) -> topcoat::Result<impl View> {
    let choices = references::place_order_input_form_choices(cx, Choices::new()).await;
    Ok(view! {
        <form method="post">
            place_order_input_fields(form: PlaceOrderInputForm::new(), choices: choices)
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
- **An item's page** renders `<type>_detail`, headed by its title, links
  to its edit and delete pages, a form per action and a section per
  resource under it, each only when the caller may.
- **The new and edit pages** render their form, the edit form filled
  from the item: each field the update's input and the item's type share
  by JSON name and kind; any other starts as the form's `new()`.
- **The delete page** shows the item and the form that deletes it, so a
  GET never deletes and no script confirms.

Every form a page renders takes its references' options, and a list's
rows and an item's detail their records' labels ([What a value refers
to](#what-a-value-refers-to)), under the app's choices; an action's form
takes its own beside the detail's labels.

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
- a [live list](#live-lists)'s shard meets its list's;
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

The rate limit is the operation's own limiter for the pages, at its
route's rate, which every page and shard that calls the operation shares,
and counts each client by its IP address as Topcoat reads it
(`client_ip`), as a procedure's does; the JSON API's route and the
procedure keep theirs, as replicas would. A page that meets several
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
same signal. A list without a filter or a page has nothing to update,
and stays a component. An action's form, a post that changes the item,
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
