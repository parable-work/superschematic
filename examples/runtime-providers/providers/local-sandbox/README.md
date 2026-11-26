# local-sandbox

A sandbox provider (D159) that runs each run's instance as a process on
this machine, for tests and `stack dev`. It isolates nothing. The command
runs with the spec's env and secrets as its environment, and the
provider logs the secrets' names, never their values. `cancel` kills the
process, and `cost` reports its wall time.

A provider for a container platform, such as Cloud Run jobs or Fly
Machines, implements the same four operations: `start`, `cancel`,
`status` and `cost`.
