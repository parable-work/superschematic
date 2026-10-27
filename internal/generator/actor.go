package generator

import "github.com/parable-work/superschematic/internal/generator/sqlutil"

// ormActor is the DB service's system actor and whether an update of a
// table with updatedBy needs an acting user (D87), from its outputs.sql,
// as both of its ORMs are generated with them.
func (r run) ormActor() sqlutil.Actor {
	system, set := r.Outputs.SystemActor()
	return sqlutil.Actor{
		SystemID:         system.ID,
		SystemName:       system.Name,
		SystemSet:        set,
		RequireUpdatedBy: r.Outputs.RequireUpdatedBy(),
	}
}
