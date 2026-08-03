package forwarder

import "testing"

func TestProviderMetricsDetectPartialCachePlateau(t *testing.T) {
	stream := &ActiveStream{}
	_, _, passes := updateProviderCachePlateauState(stream, turnUsageSnapshot{InputTokens: 200, CacheReadTokens: 100}, 1000, true)
	if passes != 0 {
		t.Fatalf("first passes=%d", passes)
	}
	for i := 1; i <= 3; i++ {
		_, _, passes = updateProviderCachePlateauState(stream, turnUsageSnapshot{InputTokens: int64(200 + i*50), CacheReadTokens: 100}, int64(1000+i*500), true)
	}
	if passes != 3 {
		t.Fatalf("plateau passes=%d, want 3", passes)
	}
	delta, ratio, passes := updateProviderCachePlateauState(stream, turnUsageSnapshot{InputTokens: 400, CacheReadTokens: 180}, 3000, true)
	if delta != 80 || ratio <= 0 || passes != 0 {
		t.Fatalf("delta=%d ratio=%f passes=%d", delta, ratio, passes)
	}
}
