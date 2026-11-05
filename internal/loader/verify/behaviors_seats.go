package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// seatsBehavior is the name of the core's Seats, which the behaviors' core
// alone implements (D104).
const seatsBehavior = "Seats"

// seatPrefix is what a by entry that names a seat of the table's Seats
// starts with (D104): seat:<name>, the core's seats::SEAT_PREFIX.
const seatPrefix = "seat:"

// seatsSeat is one seat of a Seats config as the build reads it (D104).
type seatsSeat struct {
	Max       *int64 `json:"max"`
	OnFull    string `json:"onFull"`
	Take      string `json:"take"`
	Assign    string `json:"assign"`
	FromField string `json:"fromField"`
}

// tableSeats are the seats of td's Seats use, by name, and whether td
// implements Seats. A config that does not read holds no seats: Seats'
// configSchema refuses it, which checkTypeBehaviors reports, as the core
// reads a Seats config of a shape Seats refuses as no seats.
func tableSeats(td *ir.TypeDef) (map[string]seatsSeat, bool) {
	raw, ok := implementedConfig(td, seatsBehavior)
	if !ok {
		return nil, false
	}
	var config struct {
		Seats map[string]seatsSeat `json:"seats"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &config) != nil {
		return map[string]seatsSeat{}, true
	}
	return config.Seats, true
}

// seatProblem is why the seat a by entry `seat:<name>` names is not a seat
// of td's Seats, in the core's words after the entry
// (superschematic_rules_behaviors::seats::missing_reason), or "" when it is
// one: td implements no Seats, or its Seats has no seat of the name, its
// seats listed by code point.
func seatProblem(td *ir.TypeDef, name string) string {
	seats, ok := tableSeats(td)
	if !ok {
		return fmt.Sprintf("a seat of Seats, which %s does not implement", td.Name)
	}
	if _, has := seats[name]; has {
		return ""
	}
	listed := "none"
	if len(seats) > 0 {
		listed = strings.Join(slices.Sorted(maps.Keys(seats)), ", ")
	}
	return fmt.Sprintf("which is not a seat of %s's Seats (its seats: %s)", td.Name, listed)
}

// seatOfEntry is the seat name a by entry gives after seat:, and whether it
// names a seat at all.
func seatOfEntry(entry string) (string, bool) {
	return strings.CutPrefix(entry, seatPrefix)
}

// takelessSeat reports the seat a by entry names when it is a seat of td's
// Seats that names no take permission (D104): any caller may take it, and
// so pass the by.
func takelessSeat(td *ir.TypeDef, entry string) (string, bool) {
	name, ok := seatOfEntry(entry)
	if !ok {
		return "", false
	}
	seats, _ := tableSeats(td)
	seat, has := seats[name]
	return name, has && seat.Take == ""
}

// checkSeatsConfig holds the config of a Seats use a table implements to
// the table (D104), in the core's words where the core sees the problem
// (superschematic_rules_behaviors::seats): each seat by code point, an
// onFull it names needs a max, and the fromField it names is a field of the
// table that holds a subject (string, Identity.UUID or Identity.UserID).
// What only the IR shows is the build's, in the words fieldProblem gives
// Presence's principalField: a fromField a behavior owns, a relation by its
// name, an audit field (createdBy among them: a subject the writes stamp
// is no seat's), a list or a map, and a field encrypted at rest, which the
// core reads no value of. Each problem is `type <T>: behavior Seats config:
// <reason>`, and like the core it reports the first. A config the
// configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkSeatsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Seats map[string]seatsSeat `json:"seats"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	var fields *tableFields
	for _, name := range slices.Sorted(maps.Keys(config.Seats)) {
		seat := config.Seats[name]
		if seat.OnFull != "" && seat.Max == nil {
			r.errorf(td.Owner, "type %s: behavior %s config: seats.%s.onFull names what a full seat does, and %s has no max to fill", td.Name, ref.Name, name, name)
			return
		}
		if seat.FromField == "" {
			continue
		}
		if fields == nil {
			fields = tableFieldsOf(schema, td, uses)
		}
		if why := fromFieldProblem(fields, name, seat.FromField); why != "" {
			r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
			return
		}
	}
}

// fromFieldProblem is why the fromField of the seat a Seats config names
// is not a field of the table that holds a subject, or "".
func fromFieldProblem(fields *tableFields, seat, name string) string {
	at := "seats." + seat + ".fromField"
	class, _, column, _ := fields.classify(name, false)
	if class == fieldMissing {
		return fmt.Sprintf("%s: %s is not a field of %s (its fields: %s)", at, name, fields.td.Name, fields.names())
	}
	rule := fieldRule{at: at, member: "fromField", names: "the table's own field that holds a subject", operations: true, holds: "one subject"}
	if why := fieldProblem(fields, rule, name); why != "" {
		return why
	}
	if column != nil && !slices.Contains(subjectTypes, column.Type) {
		return fmt.Sprintf("%s names %s, a %s field of %s: fromField names a field that holds a subject (%s)", at, name, column.Type, fields.td.Name, subjectTypesNamed())
	}
	return ""
}

// seatFromField is the fromField of the seat a by entry `seat:<name>`
// names on td, or "" for a field entry, a seat td's Seats lacks, and a
// seat that names none.
func seatFromField(td *ir.TypeDef, entry string) (seat, field string) {
	name, ok := seatOfEntry(entry)
	if !ok {
		return "", ""
	}
	seats, _ := tableSeats(td)
	return name, seats[name].FromField
}
