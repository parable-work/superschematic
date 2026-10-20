package cloudflare

import "testing"

// TestSeconds: a refusal writes a job's timeout as its @job writes one,
// with no zero unit, and keeps a duration's seconds.
func TestSeconds(t *testing.T) {
	for n, want := range map[int]string{
		10:   "10s",
		30:   "30s",
		90:   "1m30s",
		600:  "10m",
		900:  "15m",
		3600: "1h",
		5400: "1h30m",
		7200: "2h",
		7210: "2h0m10s",
	} {
		if got := seconds(n); got != want {
			t.Errorf("seconds(%d) = %s, want %s", n, got, want)
		}
	}
}
