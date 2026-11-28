package agents

// Path is the path of the endpoint the agent routes are under, which an
// app mounts Routes at: /api/agents for an endpoint of
// https://example.test/api/agents.
func (a *Agents) Path() string { return a.base }
