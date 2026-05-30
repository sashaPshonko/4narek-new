package main

import "time"

// События set_min_price / set_max_price от ботов (сервер), не adjustPrice.

type externalPriceEvent struct {
	Time        time.Time
	Item        string
	Kind        string // server_min | server_max
	PriceBefore int
	PriceAfter  int
}

var externalPriceEvents []externalPriceEvent

const externalPriceRetain = 72 * time.Hour

func recordExternalPriceChangeLocked(item, kind string, priceBefore, priceAfter int) {
	externalPriceEvents = append(externalPriceEvents, externalPriceEvent{
		Time:        time.Now(),
		Item:        item,
		Kind:        kind,
		PriceBefore: priceBefore,
		PriceAfter:  priceAfter,
	})
	pruneExternalPriceEventsLocked(time.Now().Add(-externalPriceRetain))
}

func pruneExternalPriceEventsLocked(cutoff time.Time) {
	i := 0
	for _, e := range externalPriceEvents {
		if e.Time.After(cutoff) {
			externalPriceEvents[i] = e
			i++
		}
	}
	externalPriceEvents = externalPriceEvents[:i]
}

func externalEventsToML(categoryType string, since, until time.Time) []mlExternalPriceEvent {
	pruneExternalPriceEventsLocked(time.Now().Add(-externalPriceRetain))
	out := make([]mlExternalPriceEvent, 0, 8)
	for _, e := range externalPriceEvents {
		cfg, ok := itemsConfig[e.Item]
		if !ok || cfg.Type != categoryType {
			continue
		}
		if !e.Time.After(since) || e.Time.After(until) {
			continue
		}
		out = append(out, mlExternalPriceEvent{
			Ts:          e.Time.UTC().Format(time.RFC3339),
			Item:        e.Item,
			Kind:        e.Kind,
			PriceBefore: e.PriceBefore,
			PriceAfter:  e.PriceAfter,
			Delta:       e.PriceAfter - e.PriceBefore,
			Note:        externalPriceEventNote(e.Kind),
		})
	}
	return out
}

func externalPriceEventNote(kind string) string {
	switch kind {
	case "server_min":
		return "Сервер/боты: минимальная цена лота — Go подставил floor. Не наше правило adjustPrice."
	case "server_max":
		return "Сервер/боты: максимальная цена — потолок лота. Не наше правило adjustPrice."
	default:
		return "Внешнее изменение цены, не adjustPrice."
	}
}
