package main

import "testing"

// Базовый вход: цена 1,0 млн, шаг 100 тыс., потолок 2 млн, пол 500 тыс., норма N=5, слотов 50, запас 1, цикл тихий.
func baseIn() pridumalnahuyInput {
	return pridumalnahuyInput{
		Price: 1_000_000, Step: 100_000, NormalSales: 5,
		BookCeil: 2_000_000, BookCeilOK: true, MultiFloor: 500_000, MultiFloorOK: true,
		TotalStockItem: 1, BuyersFreeSlots: 50,
	}
}

func TestDecideNoBookPausesBuy(t *testing.T) {
	in := baseIn()
	in.NoBook = true
	d := pridumalnahuyDecide(in)
	if !d.PauseBuy || d.NewPrice != in.Price {
		t.Fatalf("нет книги: цену держим, закупку стопаем, got %+v", d)
	}
}

func TestDecideJumpsDownToCeilingWhenAbove(t *testing.T) {
	in := baseIn()
	in.Price, in.BookCeil = 1_500_000, 1_000_000
	in.JumpConfirmed = true
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_000_000 || d.Reason != "above_ceiling_jump" {
		t.Fatalf("цена выше потолка: прыжок к потолку, got %+v", d)
	}
	in.BlockDown = true // сервер недавно поднял минимум: вниз нельзя
	if d := pridumalnahuyDecide(in); d.NewPrice < in.Price && d.Reason == "above_ceiling_jump" {
		t.Fatalf("при блоке направления прыжка нет, got %+v", d)
	}
}

func TestDecideSalesInNorm(t *testing.T) {
	in := baseIn()
	in.Sales, in.TrySells = 5, 30 // продажи в норме, попыток 6× продаж — цену не трогаем
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price || d.Reason != "sales_above_normal" {
		t.Fatalf("продажи в норме: HOLD, got %+v", d)
	}
	in.TrySells = 99 // < 20×5
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("99 < 100: HOLD, got %+v", d)
	}
	in.TrySells = 100 // 20× продаж
	in.TotalStockItem = 4 // запас ниже нормы 5 — понижать нельзя
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("запас < нормы: понижения нет, got %+v", d)
	}
	in.TotalStockItem = 5
	if d := pridumalnahuyDecide(in); d.NewPrice != 900_000 || d.Reason != "sales_above_normal_try_overheat" {
		t.Fatalf("попыток ≥ 20× продаж: шаг вниз, got %+v", d)
	}
}

func TestDecideDownWhenTriesFarAboveSalesAndStockAtNorm(t *testing.T) {
	in := baseIn()
	in.TotalStockItem = 5
	in.Sales, in.TrySells = 1, 8 // 8 > 7×1
	if d := pridumalnahuyDecide(in); d.NewPrice != 900_000 || d.Reason != "try_conversion_low" {
		t.Fatalf("попыток > 7× продаж, запас в норме: шаг вниз, got %+v", d)
	}
	in.TrySells = 7 // ровно 7× — не больше
	if d := pridumalnahuyDecide(in); d.NewPrice < in.Price {
		t.Fatalf("7 не больше 7×1: цену не роняем, got %+v", d)
	}
	in.Sales, in.TrySells = 0, 7 // при нуле продаж нужно не меньше 8 попыток
	if d := pridumalnahuyDecide(in); d.NewPrice < in.Price {
		t.Fatalf("7 попыток при нуле продаж — ещё мало, got %+v", d)
	}
	in.TrySells = 8
	if d := pridumalnahuyDecide(in); d.NewPrice != 900_000 {
		t.Fatalf("8 попыток при нуле продаж: шаг вниз, got %+v", d)
	}
	in.TotalStockItem = 4 // запас ниже нормы — по этому правилу не понижаем
	if d := pridumalnahuyDecide(in); d.Reason == "try_conversion_low" {
		t.Fatalf("запас ниже нормы: понижения нет, got %+v", d)
	}
}

func TestDecideDownStopsAtFloor(t *testing.T) {
	in := baseIn()
	in.Price, in.MultiFloor = 550_000, 500_000
	in.TotalStockItem, in.Sales, in.TrySells = 5, 0, 20
	if d := pridumalnahuyDecide(in); d.NewPrice != 500_000 {
		t.Fatalf("вниз не ниже пола, got %+v", d)
	}
	in.Price = 500_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 500_000 || d.Reason != "try_conversion_low_at_floor" {
		t.Fatalf("у пола держим, got %+v", d)
	}
}

func TestDecideUpWhenSalesLowStockLowSlotsEnough(t *testing.T) {
	in := baseIn() // продаж 0, попыток 0, запас 1 < 5, слотов 50
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_100_000 || d.Reason != "stock_below_normal" {
		t.Fatalf("продажи ниже нормы, слотов хватает: шаг вверх, got %+v", d)
	}
	in.Price = 1_950_000 // шаг вверх упирается в потолок
	if d := pridumalnahuyDecide(in); d.NewPrice != 2_000_000 {
		t.Fatalf("вверх не выше потолка, got %+v", d)
	}
	in.Price = 2_000_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 2_000_000 {
		t.Fatalf("на потолке держим, got %+v", d)
	}
}

func TestDecideHoldWhenSlotsDoNotCoverNorm(t *testing.T) {
	in := baseIn()
	in.BuyersFreeSlots = 3 // 3 + запас 1 < 5
	d := pridumalnahuyDecide(in)
	if d.NewPrice != in.Price || d.Reason != "inv_slots_tight" || d.Freeze {
		t.Fatalf("слотов не хватит довести до нормы: HOLD без фриза, got %+v", d)
	}
	in.BuyersFreeSlots = 4 // 4 + 1 = 5 хватает
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_100_000 {
		t.Fatalf("слотов хватает ровно: растим, got %+v", d)
	}
	in.BuyersFreeSlots = 0 // нет данных о ботах / никто не может купить
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price {
		t.Fatalf("слотов нет: HOLD, got %+v", d)
	}
}

func TestDecideHoldWhenStockAlreadyAtNorm(t *testing.T) {
	in := baseIn()
	in.TotalStockItem = 5
	if d := pridumalnahuyDecide(in); d.NewPrice != in.Price || d.Reason != "stock_at_norm_no_up" {
		t.Fatalf("норма уже набрана, продажи ниже нормы: HOLD, got %+v", d)
	}
}

func TestDecideJumpOnlyWhenAboveCeilingByMoreThanNacenka(t *testing.T) {
	in := baseIn()
	in.JumpConfirmed = true
	in.Price, in.BookCeil, in.Nacenka = 1_500_000, 1_000_000, 600_000
	// разрыв 500к ≤ наценки 600к: шум, прыжка нет, цена держится, рост запрещён
	d := pridumalnahuyDecide(in)
	if d.Reason == "above_ceiling_jump" || d.NewPrice > in.Price {
		t.Fatalf("разрыв меньше наценки: прыжка нет и роста нет, got %+v", d)
	}
	// разрыв ровно в наценку: тоже без прыжка
	in.Price = 1_600_000
	if d := pridumalnahuyDecide(in); d.Reason == "above_ceiling_jump" {
		t.Fatalf("разрыв == наценке: без прыжка, got %+v", d)
	}
	// разрыв 700к > наценки: прыжок к потолку
	in.Price = 1_700_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_000_000 || d.Reason != "above_ceiling_jump" {
		t.Fatalf("разрыв больше наценки: прыжок к потолку, got %+v", d)
	}
	// заморозка наценки без исходной (≥1e8): порога нет, прыжок как раньше
	in.Price, in.Nacenka = 1_100_000, 1_000_000_000
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_000_000 || d.Reason != "above_ceiling_jump" {
		t.Fatalf("наценка заморожена: порога нет, got %+v", d)
	}
}

func TestDecideNoJumpWithoutConfirmation(t *testing.T) {
	in := baseIn()
	in.Price, in.BookCeil, in.Nacenka = 1_700_000, 1_000_000, 600_000 // разрыв 700к > наценки, но только 1 цикл
	if d := pridumalnahuyDecide(in); d.Reason == "above_ceiling_jump" {
		t.Fatalf("без подтверждения (1 цикл) прыжка нет, got %+v", d)
	}
	in.JumpConfirmed = true
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_000_000 || d.Reason != "above_ceiling_jump" {
		t.Fatalf("после подтверждения — прыжок, got %+v", d)
	}
}

func TestDecideFloorJump(t *testing.T) {
	in := baseIn() // цена 1,0 млн, пол 500к
	in.Price, in.MultiFloor = 800_000, 1_000_000
	in.FloorJumpTarget = 880_000
	// не подтверждено — прыжка нет
	if d := pridumalnahuyDecide(in); d.Reason == "below_floor_jump" {
		t.Fatalf("без подтверждения прыжка к полу нет, got %+v", d)
	}
	in.FloorJumpConfirmed = true
	if d := pridumalnahuyDecide(in); d.NewPrice != 880_000 || d.Reason != "below_floor_jump" {
		t.Fatalf("подтверждено — прыжок к цели, got %+v", d)
	}
	// выключено (цель 0) — прыжка нет
	in.FloorJumpTarget = 0
	if d := pridumalnahuyDecide(in); d.Reason == "below_floor_jump" {
		t.Fatalf("прыжок выключен, got %+v", d)
	}
	// ручной максимум блокирует рост
	in.FloorJumpTarget, in.BlockUp = 880_000, true
	if d := pridumalnahuyDecide(in); d.Reason == "below_floor_jump" {
		t.Fatalf("BlockUp: прыжка нет, got %+v", d)
	}
}

func TestFloorJumpTarget(t *testing.T) {
	// минимум из пола сейчас, пола прошлого цикла и p25 продаж; не больше +10% и не выше потолка
	if got := floorJumpTarget(1_000_000, 1_200_000, 1_150_000, 1_300_000, true, 2_000_000, true); got != 1_100_000 {
		t.Fatalf("ограничение +10%%, got %d", got)
	}
	if got := floorJumpTarget(1_000_000, 1_080_000, 1_050_000, 1_020_000, true, 2_000_000, true); got != 1_020_000 {
		t.Fatalf("p25 продаж ниже пола, got %d", got)
	}
	if got := floorJumpTarget(1_000_000, 1_080_000, 0, 0, false, 1_040_000, true); got != 1_040_000 {
		t.Fatalf("не выше потолка, got %d", got)
	}
	if got := floorJumpTarget(1_000_000, 900_000, 0, 0, false, 0, false); got != 0 {
		t.Fatalf("цена выше пола — прыгать некуда, got %d", got)
	}
}

func TestDecideNightModeJumpsOnAnyGap(t *testing.T) {
	in := baseIn()
	in.Price, in.BookCeil, in.Nacenka = 1_100_000, 1_000_000, 600_000 // разрыв 100к < наценки, подтверждения нет
	if d := pridumalnahuyDecide(in); d.Reason == "above_ceiling_jump" {
		t.Fatalf("днём без подтверждения и порога прыжка нет, got %+v", d)
	}
	in.NightMode = true
	if d := pridumalnahuyDecide(in); d.NewPrice != 1_000_000 || d.Reason != "above_ceiling_jump" {
		t.Fatalf("ночью прыжок при любом разрыве, got %+v", d)
	}
}
