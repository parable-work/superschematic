# extensions/deploy

The reference deploy extension for superschematic. It registers one sidecar
document, `deploy.values.yaml`, that maps a schema's `@envVars` fields to a
Helm values file for any chart. It has no chart templates, repository URLs or
cloud-specific resources; it is the smallest useful document extension and
the starting point for a deployment integration of your own.

## The document

Next to `schema.config.*`:

```yaml
env:                       # base layer, every environment starts from it
  PORT: 9000
  API_TOKEN:               # @secret fields must be secretRefs
    secretRef: { name: example-secrets, key: api-token }
environments:              # optional overlays, one per environment
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
Kubernetes Secret.

## What the generator checks

Against the schema's `@envVars` type:

- every key is a declared field;
- every `@secret` field is a `secretRef`, never an inline value;
- every required field with no `@default` is set, per environment when the
  document declares environments, otherwise in the base layer.

The static JSON Schema (`Schema` in `deploy.go`) runs at load time and covers
shape; these three checks need the schema and run at generate time.

## What it writes

`<output-root>/deploy/values/<service>/values.yaml` for the base layer and
`values.<environment>.yaml` for each environment (base plus overlay), each a
Kubernetes container env list sorted by name:

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

## Using it

```go
reg, err := registry.Assemble(registry.DefaultNaming(), deploy.Extension{})
```

`testdata/services/example` is the worked example and `deploy_test.go` is
its acceptance test: it assembles the core plus this extension, loads the
example and asserts the three generated files byte for byte.
