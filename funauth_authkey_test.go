package main

import (
	"strings"
	"testing"
)

func TestFunauthDCProbeOrder(t *testing.T) {
	if got := funauthDCProbeOrder(0); len(got) != 5 || got[0] != 1 {
		t.Fatalf("auto order=%v", got)
	}
	if got := funauthDCProbeOrder(5); len(got) != 1 || got[0] != 5 {
		t.Fatalf("forced=%v", got)
	}
}

func TestParseFunauthSessionInputAutoDC(t *testing.T) {
	hexKey := strings.Repeat("ab", 256)
	parsed, err := parseFunauthSessionInput(hexKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Data != nil || len(parsed.Key) != 256 || parsed.ForcedDC != 0 {
		t.Fatalf("want raw key auto-dc, got data=%v key=%d dc=%d", parsed.Data != nil, len(parsed.Key), parsed.ForcedDC)
	}

	parsed, err = parseFunauthSessionInput("5:"+hexKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ForcedDC != 5 || len(parsed.Key) != 256 {
		t.Fatalf("prefix dc: %+v key=%d", parsed.ForcedDC, len(parsed.Key))
	}
}

func TestSplitAuthKeyLines(t *testing.T) {
	got := splitAuthKeyLines("a\n\n#c\nb\n")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("%v", got)
	}
}
