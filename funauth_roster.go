package main

import (
	"strings"
)

// funauthRoster — anarchy → lowercased nicks. Живые ники из bots/*.json / presence.
type funauthRoster map[int]map[string]struct{}

func (r funauthRoster) anarchyForNick(nick string) int {
	if len(r) == 0 {
		return 0
	}
	key := strings.ToLower(strings.TrimSpace(nick))
	for an, nicks := range r {
		if _, ok := nicks[key]; ok {
			return an
		}
	}
	return 0
}

func (r funauthRoster) size(anarchy int) int {
	if anarchy <= 0 {
		return 0
	}
	return len(r[anarchy])
}

func (r funauthRoster) complete(anarchy int, nickToAccount map[string]string, accountID string) bool {
	return r.completeWithVerified(anarchy, nickToAccount, nil, accountID, anarchy)
}

func (r funauthRoster) completeWithVerified(
	anarchy int,
	nickToAccount map[string]string,
	verified map[string]bool,
	accountID string,
	accountAnarchy int,
) bool {
	need := r[anarchy]
	if len(need) == 0 || accountID == "" || anarchy <= 0 {
		return false
	}
	if accountAnarchy != anarchy {
		return false
	}
	for nick := range need {
		if nickToAccount[nick] == accountID {
			continue
		}
		if verified != nil && verified[nick] {
			continue
		}
		return false
	}
	return true
}

func (r funauthRoster) progress(anarchy int, nickToAccount map[string]string, accountID string) (bound, total int) {
	return r.progressWithVerified(anarchy, nickToAccount, nil, accountID)
}

func (r funauthRoster) progressWithVerified(
	anarchy int,
	nickToAccount map[string]string,
	verified map[string]bool,
	accountID string,
) (bound, total int) {
	need := r[anarchy]
	total = len(need)
	if total == 0 || accountID == "" {
		return 0, total
	}
	for nick := range need {
		if nickToAccount[nick] == accountID {
			bound++
		} else if verified != nil && verified[nick] {
			bound++
		}
	}
	return bound, total
}

// globalProgress — по всем анархиям из roster: сколько ников уже привязано (TG или game verified).
func (r funauthRoster) globalProgress(nickToAccount map[string]string, verified map[string]bool) (bound, total int) {
	if len(r) == 0 {
		return 0, 0
	}
	for _, nicks := range r {
		for nick := range nicks {
			total++
			if nickToAccount[nick] != "" {
				bound++
				continue
			}
			if verified != nil && verified[nick] {
				bound++
			}
		}
	}
	return bound, total
}
