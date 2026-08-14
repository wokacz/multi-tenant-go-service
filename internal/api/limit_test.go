package api

import "testing"

func TestLimiterAllowsBurstThenBlocks(t *testing.T) {
	l := newLimiter(2)

	if !l.Allow("1.1.1.1") {
		t.Fatal("first request should be allowed")
	}

	if !l.Allow("1.1.1.1") {
		t.Fatal("burst of 2 should be allowed")
	}

	if l.Allow("1.1.1.1") {
		t.Fatal("third request in the same window should be blocked")
	}

	if !l.Allow("2.2.2.2") {
		t.Fatal("a different peer should have its own bucket")
	}
}

func TestNilLimiterAllows(t *testing.T) {
	var l *limiter
	if !l.Allow("1.1.1.1") {
		t.Fatal("a disabled limiter must not block")
	}
}
