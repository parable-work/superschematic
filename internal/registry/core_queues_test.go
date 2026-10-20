package registry

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueueSettingsOf: a queue's arguments with their defaults (D53). An
// unset maximum backoff is an hour, or the backoff when that is longer, and
// a retention of zero keeps no done message, which unset is a week.
func TestQueueSettingsOf(t *testing.T) {
	two := 2
	for _, tc := range []struct {
		name string
		def  *ir.QueueDef
		want QueueSettings
	}{
		{"no queue", nil, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"no arguments", &ir.QueueDef{}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"every argument", &ir.QueueDef{Retries: &two, Backoff: "10s", MaxBackoff: "5m", Lease: "2m", Retain: "24h"},
			QueueSettings{Retries: 2, BackoffSeconds: 10, MaxBackoffSeconds: 300, LeaseSeconds: 120, RetainSeconds: 86400}},
		{"a backoff over the default cap", &ir.QueueDef{Backoff: "2h"}, QueueSettings{Retries: 5, BackoffSeconds: 7200, MaxBackoffSeconds: 7200, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"a fixed backoff", &ir.QueueDef{Backoff: "1m", MaxBackoff: "1m"}, QueueSettings{Retries: 5, BackoffSeconds: 60, MaxBackoffSeconds: 60, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"no retention", &ir.QueueDef{Retain: "0s"}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 0}},
		{"no retention, bare", &ir.QueueDef{Retain: "0"}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.def != nil {
				if err := CheckQueue(tc.def); err != nil {
					t.Fatalf("CheckQueue: %v", err)
				}
			}
			if got := QueueSettingsOf(tc.def); got != tc.want {
				t.Errorf("QueueSettingsOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCheckQueueRefusesAMaxBackoffUnderTheBackoffAndANegativeRetention:
// what CheckQueue refuses of the new arguments, in every form.
func TestCheckQueueRefusesAMaxBackoffUnderTheBackoffAndANegativeRetention(t *testing.T) {
	for _, tc := range []struct {
		def  ir.QueueDef
		want string
	}{
		{ir.QueueDef{Backoff: "1m", MaxBackoff: "59s"}, `maxBackoff is "59s", shorter than the backoff of 60s`},
		{ir.QueueDef{MaxBackoff: "29s"}, `maxBackoff is "29s", shorter than the backoff of 30s`},
		{ir.QueueDef{MaxBackoff: "0s"}, `maxBackoff: "0s" is not positive`},
		{ir.QueueDef{MaxBackoff: "1.5m"}, ""},
		{ir.QueueDef{MaxBackoff: "90500ms"}, `maxBackoff: "90500ms" is not a whole number of seconds`},
		{ir.QueueDef{Retain: "-1s"}, `retain: "-1s" is negative`},
		{ir.QueueDef{Retain: "7d"}, `retain: "7d" is no duration`},
		{ir.QueueDef{Retain: "168h"}, ""},
	} {
		err := CheckQueue(&tc.def)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("CheckQueue(%+v) = %v, want nil", tc.def, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("CheckQueue(%+v) = %v, want %q", tc.def, err, tc.want)
		}
	}
}
