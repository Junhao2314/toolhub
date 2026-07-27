package store

import "testing"

func TestNormalizeJobOptions(t *testing.T) {
	defaults, err := normalizeJobOptions(JobOptions{})
	if err != nil || defaults.MaxAttempts != 5 {
		t.Fatalf("default job options = %+v, %v", defaults, err)
	}
	oneShot, err := normalizeJobOptions(JobOptions{MaxAttempts: 1, DeduplicateActive: true})
	if err != nil || oneShot.MaxAttempts != 1 || !oneShot.DeduplicateActive {
		t.Fatalf("one-shot job options = %+v, %v", oneShot, err)
	}
	for _, attempts := range []int{-1, 26} {
		if _, err := normalizeJobOptions(JobOptions{MaxAttempts: attempts}); err == nil {
			t.Fatalf("maxAttempts=%d unexpectedly accepted", attempts)
		}
	}
}
