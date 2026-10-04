package sqlmigrate

import "slices"

// Unallowed returns the plan's hazards whose class is in classes and whose
// ID allow does not list, in step order: what `migrate plan --fail-on`
// stops on. No classes means none fails.
func (p *Plan) Unallowed(classes []HazardClass, allow []string) []*Hazard {
	var out []*Hazard
	for _, hazard := range p.Hazards() {
		if slices.Contains(classes, hazard.Class) && !slices.Contains(allow, hazard.ID) {
			out = append(out, hazard)
		}
	}
	return out
}
