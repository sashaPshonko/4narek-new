package main

import "testing"

func TestFunauthRosterComplete(t *testing.T) {
	r := funauthRoster{
		503: {
			"a": {}, "b": {}, "c": {}, "d": {},
		},
	}
	nicks := map[string]string{
		"a": "tg1",
		"b": "tg1",
		"c": "tg1",
	}
	if r.complete(503, nicks, "tg1") {
		t.Fatal("expected incomplete with 3/4")
	}
	nicks["d"] = "tg1"
	if !r.complete(503, nicks, "tg1") {
		t.Fatal("expected complete with 4/4")
	}
	bound, total := r.progress(503, nicks, "tg1")
	if bound != 4 || total != 4 {
		t.Fatalf("progress got %d/%d", bound, total)
	}
}

func TestFunauthRosterCompleteWithVerified(t *testing.T) {
	r := funauthRoster{
		503: {
			"a": {}, "b": {}, "c": {}, "d": {},
		},
	}
	nicks := map[string]string{"a": "tg1"}
	verified := map[string]bool{"b": true, "c": true, "d": true}
	if !r.completeWithVerified(503, nicks, verified, "tg1", 503) {
		t.Fatal("expected complete: 1 tg bind + 3 game verified")
	}
	bound, total := r.progressWithVerified(503, nicks, verified, "tg1")
	if bound != 4 || total != 4 {
		t.Fatalf("progress got %d/%d", bound, total)
	}
	if r.completeWithVerified(503, nicks, verified, "tg1", 0) {
		t.Fatal("unassigned TG should not be full without anarchy match")
	}
}

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
