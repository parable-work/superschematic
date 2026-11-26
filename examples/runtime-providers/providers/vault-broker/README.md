# vault-broker

A credential provider (D159) over a password manager's service API. Its
settings are the API's URL, a service account token (`TOKEN`, a secret)
and the vaults a run may use (`VAULTS`). Items are named by secret
reference, `vault://<vault>/<item>/<field>`.

- `list` reads each allowed vault's items and answers their titles and
  sites. It never reads a field's value.
- `resolve` refuses a vault outside `VAULTS` and an item whose sites do not
  include the target's, reads the one field, and seals it in an
  `Injection` for a browser fill or a request header.

The service API it calls is `GET /v1/vaults/{vault}/items` and
`GET /v1/vaults/{vault}/items/{item}`, with a Bearer token. That is the
shape of 1Password Connect, which this broker could front with a token
the password manager itself limits to the named vaults. The example's
test talks to a stub of it.
