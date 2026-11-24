package main

// Какой TG-аккаунт какому боту принадлежит — для аналитики банов.
// Журнал append-only (ml_data/funauth_tg_map.jsonl): запись пишется при каждой успешной привязке и остаётся,
// даже когда TG потом удалён из пула. Для старых привязок (до журнала) при старте дописываются записи "backfill"
// из nicks.json: там есть id аккаунта, а телефон — только пока сессия ещё в пуле.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	funauthTGMapPath = "ml_data/funauth_tg_map.jsonl"
	funauthTGMapMu   sync.Mutex
)

type funauthTGMapRec struct {
	At        string `json:"at"`
	Nick      string `json:"nick"`
	AccountID string `json:"account_id"`
	Phone     string `json:"phone,omitempty"`
	Anarchy   int    `json:"anarchy,omitempty"`
	Event     string `json:"event"` // bind | backfill
}

func readTGMapLocked() []funauthTGMapRec {
	raw, err := os.ReadFile(funauthTGMapPath)
	if err != nil {
		return nil
	}
	var out []funauthTGMapRec
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r funauthTGMapRec
		if json.Unmarshal([]byte(line), &r) == nil && r.Nick != "" {
			out = append(out, r)
		}
	}
	return out
}

func appendTGMapLocked(recs ...funauthTGMapRec) {
	if len(recs) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(funauthTGMapPath), 0o755)
	f, err := os.OpenFile(funauthTGMapPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[funauth] tg-map: %v", err)
		return
	}
	defer f.Close()
	for _, r := range recs {
		b, _ := json.Marshal(r)
		_, _ = f.Write(append(b, '\n'))
	}
}

func (p *funauthPool) phoneOfLocked(accountID string) string {
	if a := p.accounts[accountID]; a != nil {
		return a.meta.Phone
	}
	return ""
}

// logTGMap — запись о привязке ника к TG (после успешного bind).
func (p *funauthPool) logTGMap(nick, accountID string, anarchy int) {
	nick = strings.ToLower(strings.TrimSpace(nick))
	if nick == "" || accountID == "" {
		return
	}
	p.mu.Lock()
	phone := p.phoneOfLocked(accountID)
	p.mu.Unlock()
	funauthTGMapMu.Lock()
	defer funauthTGMapMu.Unlock()
	appendTGMapLocked(funauthTGMapRec{
		At: time.Now().UTC().Format(time.RFC3339), Nick: nick, AccountID: accountID, Phone: phone, Anarchy: anarchy, Event: "bind",
	})
}

// backfillTGMap — один раз добавляет в журнал ники из nicks.json, которых там ещё нет.
func (p *funauthPool) backfillTGMap(phones map[string]string) {
	funauthTGMapMu.Lock()
	defer funauthTGMapMu.Unlock()
	have := map[string]bool{}
	for _, r := range readTGMapLocked() {
		have[r.Nick+"|"+r.AccountID] = true
	}
	p.mu.Lock()
	var add []funauthTGMapRec
	now := time.Now().UTC().Format(time.RFC3339)
	for nick, id := range p.nicks {
		if have[nick+"|"+id] {
			continue
		}
		phone := p.phoneOfLocked(id)
		if phone == "" {
			phone = phones[id]
		}
		add = append(add, funauthTGMapRec{At: now, Nick: nick, AccountID: id, Phone: phone, Anarchy: p.roster.anarchyForNick(nick), Event: "backfill"})
	}
	p.mu.Unlock()
	sort.Slice(add, func(i, j int) bool { return add[i].Nick < add[j].Nick })
	appendTGMapLocked(add...)
	if len(add) > 0 {
		log.Printf("[funauth] tg-map: дописано %d старых привязок", len(add))
	}
}

// tgMapView — последняя запись по каждому нику (с фильтром по подстроке ника). tgInPool — сессия TG ещё в пуле.
func (p *funauthPool) tgMapView(filter string) []map[string]any {
	filter = strings.ToLower(strings.TrimSpace(filter))
	funauthTGMapMu.Lock()
	recs := readTGMapLocked()
	funauthTGMapMu.Unlock()
	last := map[string]funauthTGMapRec{}
	count := map[string]int{}
	for _, r := range recs {
		last[r.Nick] = r // порядок файла = хронология
	}
	for _, r := range last {
		count[r.AccountID]++
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(last))
	for nick, r := range last {
		if filter != "" && !strings.Contains(nick, filter) {
			continue
		}
		phone := r.Phone
		if phone == "" {
			phone = p.phoneOfLocked(r.AccountID)
		}
		out = append(out, map[string]any{
			"nick": nick, "account_id": r.AccountID, "phone": phone, "anarchy": r.Anarchy, "at": r.At, "event": r.Event,
			"tgInPool": p.accounts[r.AccountID] != nil, "botsOnTG": count[r.AccountID],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["at"].(string)
		aj, _ := out[j]["at"].(string)
		if ai != aj {
			return ai > aj
		}
		return out[i]["nick"].(string) < out[j]["nick"].(string)
	})
	return out
}
