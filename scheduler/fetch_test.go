package scheduler

import "testing"

func TestItemGUID(t *testing.T) {
	if got := itemGUID("g", "l", "t", "c"); got != "g" {
		t.Errorf("prefers GUID, got %q", got)
	}
	if got := itemGUID("", "l", "t", "c"); got != "l" {
		t.Errorf("falls back to link, got %q", got)
	}
	a := itemGUID("", "", "first", "body")
	b := itemGUID("", "", "second", "body")
	if a == "" || a == b {
		t.Errorf("items with no GUID or link need distinct ids, got %q and %q", a, b)
	}
	if a != itemGUID("", "", "first", "body") {
		t.Error("fallback id must be stable across fetches")
	}
}
