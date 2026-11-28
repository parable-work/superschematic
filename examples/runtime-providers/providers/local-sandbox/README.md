# local-sandbox

A sandbox provider (D159) that runs each run's instance as a process on
this machine, for tests and `stack dev`. It isolates nothing. The command
runs with the spec's env and secrets as its environment, and the
provider logs the secrets' names, never their values. `cancel` kills the
process, and `cost` reports its wall time.

A provider for a container runtime implements the same four operations:
`start`, `cancel`, `status` and `cost`. It differs from this one in
keeping nothing in memory: each call after `start` asks the runtime by
the instance's id, since the dispatcher may make it from another process.
The runtime providers guide's
[Set up your own provider](https://parable-work.github.io/superschematic/guides/runtime-providers/#set-up-your-own-provider)
says how to write one from this package, name it in one environment, and
keep this one in the rest.
