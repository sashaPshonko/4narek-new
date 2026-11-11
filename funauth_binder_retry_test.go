package main

import (
	"testing"
	"time"
)

func TestFunauthRetryNextAccount(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"", false},
		{"reply_timeout", true},
		{"tg_bind_limit", false},
		{"bind_unexpected_reply", false},
		{"rpcDoRequest: rpc error code 400: CONNECTION_LAYER_INVALID", true},
		{"peer: resolve: rpcDoRequest: rpc error code 400: CONNECTION_LAYER_INVALID", true},
		{"AUTH_KEY_UNREGISTERED", true},
		{"session not authorized for authkey:fe097c6d", true},
		{"account_offline", true},
		{"USER_DEACTIVATED_BAN", true},
	}
	for _, tc := range cases {
		if got := funauthRetryNextAccount(tc.err); got != tc.want {
			t.Fatalf("funauthRetryNextAccount(%q)=%v want %v", tc.err, got, tc.want)
		}
	}
}

func TestFunauthFloodWait(t *testing.T) {
	if got := funauthFloodWait("peer: resolve: rpcDoRequest: rpc error code 420: FLOOD_WAIT (31018)"); got != 31018*time.Second {
		t.Fatalf("got %v", got)
	}
	if funauthFloodWait("reply_timeout") != 0 || funauthFloodWait("") != 0 {
		t.Fatal("не FLOOD_WAIT")
	}
}
