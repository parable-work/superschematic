---
title: Deploy extension
description: Map a schema's @envVars fields to a Helm values file with the reference deploy extension.
sidebar:
  order: 2
---

`extensions/deploy` is the reference document extension. It registers one
sidecar, `deploy.values.yaml`, that maps a schema's `@envVars` fields to a
Kubernetes container env list. It has no chart templates, repository URLs
or cloud-specific resources. Use it as the starting point for a deployment
integration of your own.

The package imports only `registry`, the same as an out-of-tree extension.

## Link it

```go
root := cli.New(cli.Config{Name: "my-schematic"}, deploy.Extension{})
```

Or assemble a registry without a CLI:

```go
reg, err := registry.Assemble(registry.DefaultNaming(), deploy.Extension{})
```

## The document

Next to `schema.config.*`:

```yaml
env:
  PORT: 9000
  API_TOKEN:
    secretRef: { name: example-secrets, key: api-token }
environments:
  staging:
    env:
      DATABASE_URL: postgres://db.staging.internal/example
      LOG_LEVEL: debug
  production:
    env:
      DATABASE_URL: postgres://db.internal/example
```

Values are strings, numbers, booleans (all emitted as strings, since a
container env value is a string) or a `secretRef` naming a key of a
Kubernetes Secret. `environments` is optional.

The schema side is an `@envVars` type:

```ts
import { Network } from "superscalar";
import { Default, Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

@envVars
export abstract class ExampleConfig {
  DATABASE_URL: Network.Url;
  API_TOKEN: Secret<string>;
  PORT: Default<number, 8080>;
  LOG_LEVEL?: string;
}
```

## What the generator checks

Against the schema's `@envVars` type:

- every key is a declared field
- every `@secret` field is a `secretRef`, never an inline value
- every required field with no `@default` is set, per environment when the
  document declares environments, otherwise in the base layer

The static JSON Schema (`Schema` in `deploy.go`) runs at load time and
covers shape. The three checks need the schema and run at generate time.

## What it writes

`<output-root>/deploy/values/<service>/values.yaml` for the base layer and
`values.<environment>.yaml` for each environment (base plus overlay), each
a Kubernetes container env list sorted by name:

```yaml
env:
    - name: API_TOKEN
      valueFrom:
        secretKeyRef:
            name: example-secrets
            key: api-token
    - name: DATABASE_URL
      value: postgres://db.internal/example
    - name: PORT
      value: "9000"
```

Most charts accept that list under `env:` or `extraEnv:`.

## Tests

`extensions/deploy/testdata/services/example` is the worked example.
`deploy_test.go` assembles the core plus this extension, loads the example
and asserts the generated files byte for byte.
