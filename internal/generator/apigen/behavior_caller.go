package apigen

// BuildsBehaviorCaller reports whether the module defines behaviorCaller,
// the caller a write through the behaviors' core runs as (D103): an API
// that serves a behavior operation (behaviors.go), or whose table
// operations write a table that composes behaviors (tables.go). It then
// holds behavior_caller.go, whose principalCaller hands the core the
// request's principal whole (D156, D158, D200).
func (o *APIOutput) BuildsBehaviorCaller() bool {
	return o.HasBehaviorEndpoints() || (o.Tables != nil && o.Tables.Rules)
}
