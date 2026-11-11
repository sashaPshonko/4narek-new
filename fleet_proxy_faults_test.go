package main

import "testing"

func TestAggregateProxyFaultsGroupsByIP(t *testing.T) {
	in := []authFaultView{
		{Username: "a", Kind: "proxy_error", IP: "1.2.3.4:8000", At: "100", Reason: "прокси недоступен"},
		{Username: "b", Kind: "proxy_error", IP: "1.2.3.4:8000", At: "200", Reason: "таймаут"},
		{Username: "c", Kind: "bad_password", IP: "9.9.9.9:8000"},
		{Username: "d", Kind: "proxy_error", IP: "5.6.7.8:8000", At: "50"},
		{Username: "e", Kind: "proxy_error"}, // без ip — пропускаем
	}
	out := aggregateProxyFaults(in)
	if len(out) != 2 {
		t.Fatalf("ждём 2 прокси, got %+v", out)
	}
	if out[0].IP != "1.2.3.4:8000" || len(out[0].Bots) != 2 || out[0].Reason != "таймаут" || out[0].At != "200" {
		t.Fatalf("первая группа: %+v", out[0])
	}
	if out[1].IP != "5.6.7.8:8000" || len(out[1].Bots) != 1 {
		t.Fatalf("вторая группа: %+v", out[1])
	}
}
