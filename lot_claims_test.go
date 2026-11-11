package main

import (
	"testing"
	"time"
)

func resetLotClaims() {
	lotClaimsMu.Lock()
	lotClaims = map[string]lotClaim{}
	lotClaimsMu.Unlock()
}

func TestFirstClaimWinsAndSecondIsDenied(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	if ok, owner := tryClaimLot("u1", "botA", now); !ok || owner != "botA" {
		t.Fatalf("первый должен получить лот: %v %q", ok, owner)
	}
	if ok, owner := tryClaimLot("u1", "botB", now.Add(10*time.Millisecond)); ok || owner != "botA" {
		t.Fatalf("второй должен получить отказ с владельцем botA: %v %q", ok, owner)
	}
	if ok, _ := tryClaimLot("u1", "botA", now.Add(time.Second)); !ok {
		t.Fatal("владелец может продлить заявку")
	}
}

func TestClaimExpiresAndRelease(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLot("u2", "botA", now)
	if ok, _ := tryClaimLot("u2", "botB", now.Add(lotClaimTTL+time.Second)); !ok {
		t.Fatal("после TTL лот свободен")
	}
	if releaseLot("u2", "botA", now.Add(lotClaimTTL+2*time.Second)) {
		t.Fatal("чужая заявка не снимается")
	}
	if !releaseLot("u2", "botB", now.Add(lotClaimTTL+2*time.Second)) {
		t.Fatal("владелец снимает свою заявку")
	}
	if ok, _ := tryClaimLot("u2", "botC", now.Add(lotClaimTTL+3*time.Second)); !ok {
		t.Fatal("после release лот свободен")
	}
}

func TestSnapshotListsOnlyLiveClaims(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLot("old", "botA", now.Add(-lotClaimTTL-time.Second))
	tryClaimLot("new", "botB", now)
	snap := lotClaimsSnapshot(now)
	if len(snap) != 1 || snap[0]["uuid"] != "new" || snap[0]["username"] != "botB" {
		t.Fatalf("snapshot: %v", snap)
	}
}

func TestFreerBotOutbidsInsideWindow(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotRanked("u3", "botA", 5, false, 2*time.Second, now)
	if ok, owner := tryClaimLotRanked("u3", "botB", 1, false, 1800*time.Millisecond, now.Add(300*time.Millisecond)); !ok || owner != "botB" {
		t.Fatalf("более свободный бот должен получить лот: %v %q", ok, owner)
	}
	if ok, owner := tryClaimLotHeld("u3", "botA", 5, now.Add(400*time.Millisecond)); ok || owner != "botB" {
		t.Fatalf("прежний владелец не отбирает обратно: %v %q", ok, owner)
	}
}

func TestEqualOrFullerBotDoesNotOutbid(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotHeld("u4", "botA", 3, now)
	if ok, _ := tryClaimLotHeld("u4", "botB", 3, now.Add(100*time.Millisecond)); ok {
		t.Fatal("при равенстве остаётся первый")
	}
	if ok, _ := tryClaimLotHeld("u4", "botC", 6, now.Add(100*time.Millisecond)); ok {
		t.Fatal("более полный не перебивает")
	}
}

func TestNoOutbidAfterWindow(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotHeld("u5", "botA", 6, now)
	if ok, owner := tryClaimLotHeld("u5", "botB", 0, now.Add(lotClaimWindow+time.Millisecond)); ok || owner != "botA" {
		t.Fatalf("после окна первый владеет лотом: %v %q", ok, owner)
	}
}

func TestUnknownHeldNeverOutbids(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotHeld("u6", "botA", 4, now)
	if ok, _ := tryClaimLot("u6", "botB", now.Add(50*time.Millisecond)); ok {
		t.Fatal("заявка без held не перебивает")
	}
}

func TestNotFullAHBeatsFullAHRegardlessOfHeld(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	// первый — бот с полным АХ и пустым инвентарём (накоплено 5)
	tryClaimLotRanked("u7", "full", 5, true, 2*time.Second, now)
	// бот с неполным АХ, но накоплено больше (9) — всё равно лучше
	if ok, owner := tryClaimLotRanked("u7", "free", 9, false, 1500*time.Millisecond, now.Add(600*time.Millisecond)); !ok || owner != "free" {
		t.Fatalf("бот с неполным АХ должен перебить полного в окне: %v %q", ok, owner)
	}
}

func TestFullAHDoesNotOutbidAndWindowEnds(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotRanked("u8", "free", 4, false, 2*time.Second, now)
	if ok, _ := tryClaimLotRanked("u8", "full", 0, true, 2*time.Second, now.Add(50*time.Millisecond)); ok {
		t.Fatal("полный АХ не перебивает неполный")
	}
	resetLotClaims()
	tryClaimLotRanked("u9", "full", 5, true, 2*time.Second, now)
	if ok, _ := tryClaimLotRanked("u9", "free", 0, false, 2*time.Second, now.Add(lotClaimWindow+time.Millisecond)); ok {
		t.Fatal("после окна владелец окончателен")
	}
}

func TestOutbidOnlyIfNewBotClicksNotLater(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	// A кликнет через 2 с от начала. B (свободнее) заявился через 0,5 с и кликнет ещё через 2 с — то есть на 0,5 с позже A.
	tryClaimLotRanked("u10", "A", 5, false, 2*time.Second, now)
	if ok, owner := tryClaimLotRanked("u10", "B", 0, false, 2*time.Second, now.Add(500*time.Millisecond)); ok || owner != "A" {
		t.Fatalf("B кликнет позже A — лот остаётся у A, чтобы не потерять его: %v %q", ok, owner)
	}
	// C кликнет раньше A — уступаем
	if ok, owner := tryClaimLotRanked("u10", "C", 0, false, 1200*time.Millisecond, now.Add(500*time.Millisecond)); !ok || owner != "C" {
		t.Fatalf("C кликает раньше A — перебивает: %v %q", ok, owner)
	}
}

func TestOutbidNeedsKnownClickTimes(t *testing.T) {
	resetLotClaims()
	now := time.Now()
	tryClaimLotRanked("u11", "A", 5, false, 2*time.Second, now)
	if ok, _ := tryClaimLotRanked("u11", "B", 0, false, clickUnknown, now.Add(100*time.Millisecond)); ok {
		t.Fatal("неизвестное время клика не перебивает")
	}
	resetLotClaims()
	tryClaimLotRanked("u12", "A", 5, false, clickUnknown, now)
	if ok, _ := tryClaimLotRanked("u12", "B", 0, false, time.Second, now.Add(100*time.Millisecond)); ok {
		t.Fatal("если у владельца время клика неизвестно, не рискуем")
	}
}
