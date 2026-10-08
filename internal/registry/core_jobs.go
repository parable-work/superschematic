package registry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	// The IANA time zones a job's schedule names, built in, so a time zone
	// checks the same on every machine and in every image.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	ir "github.com/parable-work/superschematic/ir"
)

// jobDecorator returns @job, from @superschematic/api and only in API
// schemas: a class of the schema that declares a job of the API (D52). The
// class holds no fields and is no type: Apply records the job in
// Schema.Jobs, and the TypeScript reader leaves the class out of Types.
func jobDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "job", Packages: []string{pkgAPI}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			job, err := jobOf(n.Type, args)
			if err != nil {
				return err
			}
			if n.Schema.Job(job.Name) != nil {
				return fmt.Errorf("@job class %s is declared twice", job.Name)
			}
			n.Schema.Jobs = append(n.Schema.Jobs, job)
			return nil
		},
	}
}

// jobArgs are the arguments of @job, all optional.
type jobArgs struct {
	Schedule *string `json:"schedule"`
	TimeZone *string `json:"timeZone"`
	Timeout  *string `json:"timeout"`
	Retries  *any    `json:"retries"`
}

// jobOf reads @job({ schedule, timeZone, timeout, retries }) on td. Errors
// in the object are ArgErrors, so the frontend points at it.
func jobOf(td *ir.TypeDef, args []any) (*ir.Job, error) {
	if len(td.Fields) > 0 {
		return nil, fmt.Errorf("@job class %s has fields; a job takes no input, so its class holds none", td.Name)
	}
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@job class %s extends or implements another class; a job's class declares the job and nothing else", td.Name)
	}
	job := &ir.Job{Name: td.Name, Comment: td.Comment}
	if len(args) > 1 {
		return nil, fmt.Errorf("@job takes at most one options object")
	}
	if len(args) == 0 || args[0] == nil {
		return job, nil
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@job takes an options object: { schedule, timeZone, timeout, retries }")
	}
	for key := range obj {
		switch key {
		case "schedule", "timeZone", "timeout", "retries":
		default:
			return nil, ArgErrorf(0, "@job has no option %q; it takes schedule, timeZone, timeout and retries", key)
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, ArgErrorf(0, "@job options: %v", err)
	}
	var a jobArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, ArgErrorf(0, "@job options: schedule, timeZone and timeout are strings and retries a whole number: %v", err)
	}
	if a.Schedule != nil {
		job.Schedule = *a.Schedule
	}
	if a.TimeZone != nil {
		job.TimeZone = *a.TimeZone
	}
	if a.Timeout != nil {
		job.Timeout = *a.Timeout
	}
	if a.Retries != nil {
		n, ok := (*a.Retries).(float64)
		if !ok || n != float64(int(n)) {
			return nil, ArgErrorf(0, "@job retries is %v; it is a whole number", *a.Retries)
		}
		job.Retries = int(n)
	}
	if err := CheckJob(job); err != nil {
		return nil, ArgErrorf(0, "@job %s: %v", job.Name, err)
	}
	return job, nil
}

// CheckJob checks a job's declaration in every form: a schedule that is a
// five-field cron, a time zone the IANA database names, a timeout of whole
// seconds, and retries that are not negative.
func CheckJob(job *ir.Job) error {
	if job.Name == "" {
		return fmt.Errorf("a job has no name")
	}
	if job.Schedule != "" {
		if err := CheckSchedule(job.Schedule); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
	}
	if job.TimeZone != "" {
		if err := CheckTimeZone(job.TimeZone); err != nil {
			return fmt.Errorf("timeZone: %w", err)
		}
	}
	if job.Timeout != "" {
		if _, err := TimeoutSeconds(job.Timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	if job.Retries < 0 {
		return fmt.Errorf("retries is %d; it is zero or more", job.Retries)
	}
	return nil
}

// scheduleParser reads a five-field cron and nothing else: no seconds
// field, and no descriptor such as @hourly, which Cloud Scheduler does not
// read.
var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseSchedule reads a job's schedule, a five-field cron (minute, hour,
// day of the month, month and day of the week), in loc.
func ParseSchedule(schedule string, loc *time.Location) (cron.Schedule, error) {
	if err := CheckSchedule(schedule); err != nil {
		return nil, err
	}
	return scheduleParser.Parse("CRON_TZ=" + loc.String() + " " + schedule)
}

// CheckSchedule checks that schedule is a five-field cron: minute, hour,
// day of the month, month and day of the week, as unix cron and Cloud
// Scheduler read it. A time zone goes in the job's timeZone, not in the
// schedule.
func CheckSchedule(schedule string) error {
	fields := strings.Fields(schedule)
	switch {
	case strings.HasPrefix(schedule, "TZ=") || strings.HasPrefix(schedule, "CRON_TZ="):
		return fmt.Errorf("%q names a time zone; set the job's timeZone instead", schedule)
	case strings.HasPrefix(schedule, "@"):
		return fmt.Errorf("%q is a descriptor; write the five fields: minute, hour, day of the month, month and day of the week", schedule)
	case len(fields) != 5:
		return fmt.Errorf("%q has %d fields; a schedule has five: minute, hour, day of the month, month and day of the week", schedule, len(fields))
	}
	if _, err := scheduleParser.Parse(schedule); err != nil {
		return fmt.Errorf("%q: %w", schedule, err)
	}
	return nil
}

// CheckTimeZone checks that zone is a time zone the IANA database names
// (`UTC`, `Europe/Paris`). The database is built in.
func CheckTimeZone(zone string) error {
	if zone == "" || zone == "Local" {
		return fmt.Errorf("%q is no IANA time zone", zone)
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return fmt.Errorf("%q is no IANA time zone", zone)
	}
	return nil
}

// TimeoutSeconds reads a job's timeout, a positive duration of whole
// seconds as Go writes one (`90s`, `10m`, `1h30m`), in seconds.
func TimeoutSeconds(timeout string) (int, error) {
	d, err := time.ParseDuration(timeout)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is no duration; write one as `90s`, `10m` or `1h30m`", timeout)
	case d <= 0:
		return 0, fmt.Errorf("%q is not positive", timeout)
	case d%time.Second != 0:
		return 0, fmt.Errorf("%q is not a whole number of seconds", timeout)
	}
	return int(d / time.Second), nil
}
