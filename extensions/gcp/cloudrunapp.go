package gcp

import (
	"fmt"

	"github.com/parable-work/superschematic/registry"
)

// The app platform (D78): an app is the project's own HTTP program over
// its API's Deps, which runs as a server does, as a Cloud Run service of
// its own (lowerRunService). It has its own service account, its API's
// Cloud SQL volume and egress, and public ingress when the stack exposes
// it. Its edges are its API's, connected by the app platform's connectors
// as a server's are.

// appAddress is an app's run.app URL, as a server's (serviceAddress), or
// none for an app the environment turns off, which has no service.
func appAddress(ctx registry.PlatformContext) any {
	if app := ctx.Deployable.App; app == nil || !app.Enabled {
		return nil
	}
	return serviceAddress(ctx)
}

// appPublicAddress is where a browser reaches an exposed app, as an exposed
// server (servicePublicAddress), or none for an app turned off.
func appPublicAddress(ctx registry.PlatformContext) any {
	if app := ctx.Deployable.App; app == nil || !app.Enabled {
		return nil
	}
	return servicePublicAddress(ctx)
}

// lowerApp lowers an app to a Cloud Run service, as a server is lowered
// (lowerService), with one difference: the app's program answers none of
// the entrypoint's health paths, so an instance takes traffic once it
// accepts a connection on its port (portProbe). An app the environment
// turns off lowers to its account, the grants and secrets its bindings
// need and no service, so nothing references the service's address.
func lowerApp(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.App == nil {
		return registry.Lowered{}, fmt.Errorf("app %s has no run", d.Name)
	}
	if !d.App.Enabled {
		out, _, err := lowerWorkload(ctx)
		return out, err
	}
	return lowerRunService(ctx, portProbe)
}

// portProbe is an app's health checks: a TCP connection to its port, as
// its startup probe on the startup probe's schedule, Cloud Run's own check
// for a service that sets none, and as its liveness probe on a server's
// liveness schedule, so an instance that stops accepting connections is
// restarted. TCP is all an app offers to probe: its program, the
// project's serve, owns every route, so it answers neither /readyz nor
// /healthz, and the entrypoint serves no route of its own beside it.
// Its entrypoint listens only once its engine has opened on the document
// it was built with, so the startup probe fails a revision whose
// document is not live. A TCP probe passes once the program listens,
// whether or not it accepts, and whatever its pools reach.
func portProbe(container map[string]any) {
	container["startupProbe"] = map[string]any{
		"tcpSocket":        map[string]any{"port": containerPort},
		"periodSeconds":    startupPeriod,
		"timeoutSeconds":   startupTimeout,
		"failureThreshold": startupFailures,
	}
	container["livenessProbe"] = map[string]any{
		"tcpSocket":        map[string]any{"port": containerPort},
		"periodSeconds":    livenessPeriod,
		"timeoutSeconds":   livenessTimeout,
		"failureThreshold": livenessFailures,
	}
}
