package cloudflare

import (
	"strings"
	"testing"
)

// TestCheckBucketNameRefusesBootstrapsBuckets: no Bucket service's bucket
// takes the name of a bucket bootstrap makes in every account. Every name
// the platform gives holds the stack, the environment and the bucket, so
// none does today, and the check keeps it so.
func TestCheckBucketNameRefusesBootstrapsBuckets(t *testing.T) {
	for _, name := range []string{StateBucket, BootstrapBucket} {
		err := checkBucketName("media", name, nil)
		if err == nil || !strings.Contains(err.Error(), "would be named "+name+", a bucket bootstrap keeps an account's state in") {
			t.Errorf("a bucket named %s: %v", name, err)
		}
	}
	if err := checkBucketName("media", "acme-staging-media", nil); err != nil {
		t.Errorf("acme-staging-media: %v", err)
	}
}
