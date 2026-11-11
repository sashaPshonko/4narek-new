package main

import "testing"

func TestFunauthRosterGlobalProgress(t *testing.T) {
	r := funauthRoster{
		502: {"a": {}, "b": {}},
		503: {"c": {}, "d": {}},
	}
	nicks := map[string]string{"a": "tg1", "c": "tg2"}
	verified := map[string]bool{"d": true}
	bound, total := r.globalProgress(nicks, verified)
	if total != 4 || bound != 3 {
		t.Fatalf("global progress got %d/%d want 3/4", bound, total)
	}
}
