package notify

import (
	"fmt"
	"time"
)

// Quiet hours in a recipient's zone (D153, #602), as the TypeScript
// runtime's quiet-hours.ts reads them. A window [start, end) of wall
// times, which one that crosses midnight reads as one, starts and ends at
// instants: each wall time on its date in the zone is the first instant
// whose wall time reaches it. When the clocks go forward past it, that is
// the instant of the jump; when they go back over it, the first time the
// clock shows it. So the hours a window holds a message are the hours
// that pass, one fewer on the night the clocks go forward and one more on
// the night they go back.

const dayMillis = 86_400_000

// wallMillis is the wall time of instant ms in loc as the milliseconds
// since the epoch the same wall time is in UTC, to the second.
func wallMillis(ms int64, loc *time.Location) int64 {
	at := time.UnixMilli(ms).In(loc)
	return time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), 0, time.UTC).UnixMilli()
}

// offsetMillis is loc's offset from UTC at instant ms.
func offsetMillis(ms int64, loc *time.Location) int64 {
	return wallMillis(ms, loc) - ms/1000*1000
}

// instantOfWall is the first instant whose wall time in loc reaches wall
// (the milliseconds since the epoch the wall time is in UTC).
func instantOfWall(wall int64, loc *time.Location) int64 {
	before := offsetMillis(wall-dayMillis, loc)
	after := offsetMillis(wall+dayMillis, loc)
	first := int64(0)
	found := false
	for _, at := range []int64{wall - before, wall - after} {
		if wallMillis(at, loc) == wall && (!found || at < first) {
			first, found = at, true
		}
	}
	if found {
		return first
	}
	// A wall time the clocks skip: the instant of the jump.
	lo, hi := wall-max(before, after), wall-min(before, after)
	for hi-lo > 1000 {
		mid := lo + (hi-lo)/2000*1000
		if wallMillis(mid, loc) >= wall {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// QuietDelayAt is how many seconds from at until the quiet hours [start,
// end) of loc end, or 0 when at is outside them: the window of the day
// before at's and of at's own day, each from the instant its start is
// reached to the instant its end is.
func QuietDelayAt(at time.Time, loc *time.Location, start, end string) int {
	from, okFrom := minutesOf(start)
	to, okTo := minutesOf(end)
	if !okFrom || !okTo || from == to {
		return 0
	}
	ms := at.UnixMilli()
	wall := wallMillis(ms, loc)
	today := wall - ((wall%dayMillis)+dayMillis)%dayMillis
	for _, day := range []int64{today - dayMillis, today} {
		opens := instantOfWall(day+int64(from)*60_000, loc)
		next := int64(0)
		if from > to {
			next = dayMillis
		}
		closes := instantOfWall(day+next+int64(to)*60_000, loc)
		if ms >= opens && ms < closes {
			return int((closes - ms + 999) / 1000)
		}
	}
	return 0
}

// quietDelay is QuietDelayAt in the zone named zone, from Go's zone
// database, which the binary carries (tzdata.go) on Postgres and SQLite
// alike.
func quietDelay(at time.Time, zone, start, end string) (int, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return 0, fmt.Errorf("notify: the quiet hours in %s: %w", zone, err)
	}
	return QuietDelayAt(at, loc, start, end), nil
}
