---
title: Platform extension
description: Add a schema kind that groups other services, with a decorator, a verify rule and a catalog generator.
sidebar:
  order: 3
---

`extensions/platform` is a worked example of a kind extension. It adds
`Platform`: a service whose schema files declare named platforms, each
grouping other services under a visibility and listing the resources those
services share. It registers the kind, one decorator (`@platform` from
`@superschematic/platform`), a verify rule and a generator that writes a
JSON catalog.

It stays small on purpose: three constants, one struct, one `Register`
function, one verify rule, one generator. A production version of the same
kind would reference member services through their sentinels
(`service({...})` values from the config package) instead of bare names
and close the set of shared resource kinds. Both are
`DecoratorSpec.Args` and `KindSpec.Verify` changes, not engine changes.

## Link it

```go
root := cli.New(cli.Config{Name: "my-schematic"}, platform.Extension{})
```

Registering `@platform` from `@superschematic/platform` makes that package
an authoring package. Ship the TypeScript half next to the Go package
(`extensions/platform/packages/platform`) and put it on the module path
the way acme does for `@acme/schema`.

## Author a platform schema

TypeScript:

```ts
import { platform } from "@superschematic/platform";

@platform({
  description: "Customer-facing API and its database",
  visibility: "public",
  services: ["api", "db"],
  shared: { database: ["api", "db"] }
})
export abstract class Core {}
```

The same in the data form:

```yaml
types:
  Core:
    name: Core
    role: EmbeddedStruct
    extensions:
      platform:
        platform:
          visibility: public
          services: [api, db]
          shared: { database: [api, db] }
```

`visibility` is `public` or `internal`. `services` is a non-empty list of
names. `shared` maps a resource kind (any name you choose) to the members
that share one instance of it.

`schema.config` sets `kind: "Platform"` (a string; the core enum does not
list it). The kind sets `NoSentinel: true` because a platform schema
groups services; it is not a service others import.

## What Register does

`KindSpec.Pipeline` is `["platformCatalog"]` only; a Platform schema does
not run the core type generator. The argument schema already closes the
visibility set and requires at least one member. `KindSpec.Verify` walks
every `@platform` and rejects a `shared` member that is not in
`services`.

The decorator targets types on kind `Platform`. `Apply` writes the decoded
argument into `TypeDef.Extensions["platform"]`. `platform.Of(td)` is the
read side.

The generator writes one JSON file per schema under
`<output-root>/platforms/<service>/catalog.json`: the service name and
every platform it found.

## Tests

`extensions/platform/testdata/services/fleet` is a valid schema.
`fleet-yaml` is the same in YAML. `broken-fleet` fails verify.
`platform_test.go` is the acceptance test.
