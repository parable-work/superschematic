package ir

import "strings"

// Job is a job an API service declares with `@job` on a class of its
// schema: a run to completion that does the API's background work with the
// API's connections and clients (docs/stack-model.md, section 8.7, D52).
// The class's name is the job's, and the class holds nothing else, so a job
// is no type: the loaders record it in Schema.Jobs and in no other place.
//
// The decorator's arguments are all optional. A job with no Schedule runs
// only on demand. An environment's settings change the schedule or turn it
// off (DeployableSettings), and resolution records what runs on the job's
// deployable (ResolvedJob).
type Job struct {
	// Name is the class's name: the method of the API's generated Jobs
	// interface that runs the job.
	Name string `json:"name" yaml:"name"`

	// Comment stores the node-attached comment of the class declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Schedule is a five-field cron: minute, hour, day of the month, month
	// and day of the week (`*/15 * * * *`). Empty runs the job only on
	// demand.
	Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty"`

	// TimeZone is the IANA time zone Schedule is read in. Empty is UTC.
	TimeZone string `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`

	// Timeout bounds one run, a duration of whole seconds as Go writes one
	// (`10m`, `1h30m`). Empty is DefaultJobTimeoutSeconds.
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// Retries is how many times a failed run is run again before it
	// fails. Zero runs it once.
	Retries int `json:"retries,omitempty" yaml:"retries,omitempty"`
}

// DefaultJobTimeoutSeconds bounds a run of a job that sets no timeout: ten
// minutes, Cloud Run's default for a task.
const DefaultJobTimeoutSeconds = 600

// DefaultJobTimeZone is the time zone of a schedule that names none.
const DefaultJobTimeZone = "UTC"

// Job returns the job named name, or nil.
func (s *Schema) Job(name string) *Job {
	for _, job := range s.Jobs {
		if job != nil && job.Name == name {
			return job
		}
	}
	return nil
}

// JobDeployableName names the deployable of job of the API service api: the
// API's name, a hyphen, and the job's name in kebab case
// (`shop-orders-expire-carts`). A job takes its API's name, not that of the
// server that serves the API, so grouping APIs into an `@server` leaves
// the job's name, and every resource named after it, as it was; and two
// APIs of one server may each declare a job of the same name (D52).
func JobDeployableName(api, job string) string {
	return api + "-" + Kebab(job)
}

// Kebab lowercases a name and joins its words with hyphens: ExpireCarts is
// expire-carts, SendHTTPDigest is send-http-digest, and expire_carts is
// expire-carts.
func Kebab(name string) string {
	upper := func(i int) bool { return i >= 0 && i < len(name) && name[i] >= 'A' && name[i] <= 'Z' }
	lower := func(i int) bool {
		return i >= 0 && i < len(name) && (name[i] >= 'a' && name[i] <= 'z' || name[i] >= '0' && name[i] <= '9')
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case upper(i):
			if lower(i-1) || upper(i-1) && lower(i+1) {
				b.WriteByte('-')
			}
			b.WriteByte(c - 'A' + 'a')
		case c == '_':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
