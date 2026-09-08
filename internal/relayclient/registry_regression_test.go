package relayclient

import "testing"

func TestCancellationReservesIDUntilHandlerFinishes(t *testing.T) {
	registry := newRequestRegistry()
	registry.Add("pending", func() {})
	registry.Cancel("pending")
	if registry.Add("pending", func() {}) {
		t.Fatal("cancelled but unfinished request ID was reused")
	}
	registry.Remove("pending")
	if !registry.Add("pending", func() {}) {
		t.Fatal("finished request ID was not released")
	}
}
