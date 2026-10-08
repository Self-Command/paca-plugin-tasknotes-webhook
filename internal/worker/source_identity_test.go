package worker

import (
	"testing"
	"time"
)

func TestParentAssociationWaitingIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if associationWaitExhausted(8, now.Add(-29*time.Minute), now) {
		t.Fatal("an eligible pending event must keep waiting")
	}
	if !associationWaitExhausted(9, now, now) {
		t.Fatal("tenth attempt must become visible pending association")
	}
	if !associationWaitExhausted(1, now.Add(-30*time.Minute), now) {
		t.Fatal("thirty minute deadline must bound retries independently")
	}
}
