package main

import (
	"sort"
	"strconv"
	"strings"
)

// Книга АХ хранит зачарования компактно: словарь ench_names (имя → id) и в лоте строка "id:lvl,id:lvl".
// Раньше в каждой строке лежал JSON с полными именами (~250 байт × 7 млн лотов ≈ 1.4 ГБ).

var enchNameIDs = map[string]int64{}

func ensureEnchNamesTableLocked() error {
	_, err := mlDB.Exec(`CREATE TABLE IF NOT EXISTS ench_names (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE
)`)
	return err
}

// enchNameIDLocked — id имени зачарования (создаёт при необходимости). Вызывать под mlDBMu.
func enchNameIDLocked(name string) int64 {
	if id, ok := enchNameIDs[name]; ok {
		return id
	}
	if _, err := mlDB.Exec(`INSERT OR IGNORE INTO ench_names (name) VALUES (?)`, name); err != nil {
		return 0
	}
	var id int64
	if err := mlDB.QueryRow(`SELECT id FROM ench_names WHERE name = ?`, name).Scan(&id); err != nil {
		return 0
	}
	enchNameIDs[name] = id
	return id
}

// encodeEnchantsCompactLocked — "id:lvl,id:lvl" по возрастанию id; пусто → "".
func encodeEnchantsCompactLocked(effects []ItemEffect) string {
	if len(effects) == 0 {
		return ""
	}
	type pair struct {
		id  int64
		lvl int
	}
	ps := make([]pair, 0, len(effects))
	for _, e := range effects {
		if e.Name == "" {
			continue
		}
		id := enchNameIDLocked(e.Name)
		if id == 0 {
			continue
		}
		ps = append(ps, pair{id, e.Lvl})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(p.id, 10))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(p.lvl))
	}
	return b.String()
}
