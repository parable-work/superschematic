package sqlmigrate

import (
	"fmt"
	"strings"
)

// diffRoles keeps the roles @seedRoles declares in step (D88). A new one
// is inserted in expand, with no hazard: the insert leaves a role of its
// name alone unless it has no permissions. One whose permissions change is
// updated, in expand when it only gains permissions and in contract when
// it loses one, since a server of the previous version may still serve a
// route the lost permission admits. The update is destructive: it sets
// the permissions the role holds, which an administrator may have changed
// since. A role no seed declares any more stays, since users may hold it.
// A role's change names no table, so a SQLite rebuild of its table never
// takes it in: it runs after the rebuild, at its own rank.
func (d *differ) diffRoles() {
	from := map[string]*RoleSeed{}
	for _, r := range d.from.Roles {
		from[r.Table+"/"+r.Name] = r
	}
	for _, tr := range d.to.Roles {
		subject := roleSubject(tr.Table, tr.Name)
		fr := from[d.renames.prevTable(tr.Table)+"/"+tr.Name]
		if fr == nil {
			d.add(&change{op: opSeedRole, phase: Expand, subject: subject, role: tr})
			continue
		}
		gained, lost := minus(tr.Permissions, fr.Permissions), minus(fr.Permissions, tr.Permissions)
		if len(gained) == 0 && len(lost) == 0 {
			continue
		}
		phase := Expand
		if len(lost) > 0 {
			phase = Contract
		}
		c := d.add(&change{op: opUpdateRole, phase: phase, subject: subject, role: tr, oldRole: fr})
		var what []string
		if len(gained) > 0 {
			what = append(what, "gains "+strings.Join(gained, ", "))
		}
		if len(lost) > 0 {
			what = append(what, "loses "+strings.Join(lost, ", "))
		}
		c.addHazard(HazardDestructive, "", fmt.Sprintf(
			"The role %s %s: the step sets its permissions to the seed's (%s), replacing any an administrator gave it since it was seeded.",
			tr.Name, strings.Join(what, " and "), strings.Join(tr.Permissions, ", ")))
	}
}
