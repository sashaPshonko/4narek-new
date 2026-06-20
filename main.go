package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	timezone = "Asia/Tashkent"
)

type PriceUpdate struct {
	Prices  map[string]int   `json:"prices"`
	Catalog []CatalogItemOut `json:"catalog,omitempty"`
}

type CatalogItemOut struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Type             string       `json:"type"`
	Nacenka          int          `json:"nacenka"`
	Num              int          `json:"num"`
	Effects          []ItemEffect `json:"effects"`
	ForbiddenEffects []ItemEffect `json:"forbidden_effects,omitempty"`
	MaxEffects       []ItemEffect `json:"max_effects,omitempty"`
	LoreMatch        string       `json:"lore_match,omitempty"`
}

type ItemEffect struct {
	Name string `json:"name"`
	Lvl  int    `json:"lvl"`
}

type PriceRecord struct {
	Price int       `json:"price"`
	Time  time.Time `json:"time"`
}

type PriceHistory struct {
	Records []PriceRecord `json:"records"`
	Limit   int           `json:"limit"`
}

var priceHistory = make(map[string]*PriceHistory)
const priceHistoryLimit = 30

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

var (
	clientItems      = make(map[*websocket.Conn]map[string]int)
	clientInventory  = make(map[*websocket.Conn]map[string]int)
	clientActiveTypes  = make(map[*websocket.Conn]map[string]struct{})
	clientFleetTypes   = make(map[*websocket.Conn]map[string]struct{})
	clientBotsPerType  = make(map[*websocket.Conn]map[string]int)
)

var itemLimit = map[string]int{
	"netherite_sword":    24 * 3,
	"netherite_leggings": 28 * 3,
}

var inventoryLimit = map[string]int{
	"netherite_sword":    28 * 3 * 3,
	"netherite_leggings": 28 * 3,
}

type ItemConfig struct {
	ID               string
	Name             string
	Type             string
	BasePrice        int
	NormalSales      int
	NormalCount      int
	PriceStep        int
	AnalysisTime     time.Duration
	Nacenka          int
	NacenkaMin       int
	Num              int
	Effects          []ItemEffect
	ForbiddenEffects []ItemEffect
	MaxEffects       []ItemEffect
	LoreMatch        string
}

type DailyData struct {
	Date         string                     `json:"date"`
	Prices       map[string]int             `json:"prices"`
	Nacenkas     map[string]int             `json:"nacenkas"`
	AdjustState  map[string]ItemAdjustState `json:"adjust_state"`
	BuyStats     map[string]int             `json:"buy_stats"`
	SellStats    map[string]int             `json:"sell_stats"`
	TrySellStats map[string]int             `json:"try_sell_stats"`
	MessageID    int                        `json:"message_id"`
	BuySum       map[string]int             `json:"buy_sum"`
	SellSum      map[string]int             `json:"sell_sum"`
}

var itemsConfig map[string]ItemConfig

type TradeLog struct {
	Time  time.Time
	Type  string
	Price int
}

type Data struct {
	Prices           map[string]int
	Nacenkas         map[string]int
	AdjustState      map[string]ItemAdjustState
	BuyStats         map[string]int
	SellStats        map[string]int
	TrySellStats     map[string]int
	LastTrade        map[string]time.Time
	TradeHistory     map[string][]TradeLog
	BuySum           map[string]int
	SellSum          map[string]int
	LastManualUpdate map[string]time.Time
}

var (
	data    = &Data{}
	mutex   = sync.RWMutex{}
	clients = make(map[*websocket.Conn]bool)

	currentDay string
	dailyData  DailyData

	swordTimes = make(map[string]time.Time)

	lastPriceUpdate = make(map[string]time.Time)

	typeActiveSince  = make(map[string]time.Time)
	lastTypePresence = make(map[string]time.Time)

	broadcast = make(chan interface{}, 1000)

	jsonCache    = make(map[string]time.Time)
	jsonCacheMu  sync.RWMutex
	jsonCacheTTL = 5 * time.Second
)

func main() {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("Error loading location: %v", err)
		os.Exit(1)
	}

	if err := loadItemsConfig(); err != nil {
		log.Fatalf("%v", err)
	}

	data.Prices = make(map[string]int)
	data.Nacenkas = make(map[string]int)
	data.AdjustState = make(map[string]ItemAdjustState)
	data.BuyStats = make(map[string]int)
	data.SellStats = make(map[string]int)
	data.TrySellStats = make(map[string]int)
	data.LastTrade = make(map[string]time.Time)
	data.TradeHistory = make(map[string][]TradeLog)
	data.BuySum = make(map[string]int)
	data.SellSum = make(map[string]int)
	data.LastManualUpdate = make(map[string]time.Time)

	loadDailyData(loc)
	initMLLog()

	go broadcastBroker()
	go startCacheCleanup()

	http.HandleFunc("/ws", handleConnections)
	go func() {
		log.Println("Server started on :8080")
		log.Print(http.ListenAndServe(":8080", nil))
	}()

	go checkDayChange(loc)
	go startItemTimers()

	select {}
}

func filterPrices() PriceUpdate {
	filteredPrices := make(map[string]int)
	for k, v := range data.Prices {
		if _, ok := itemsConfig[k]; ok {
			filteredPrices[k] = v
		}
	}
	return PriceUpdate{Prices: filteredPrices}
}

func buildCatalogOut() []CatalogItemOut {
	out := make([]CatalogItemOut, 0, len(itemsConfig))
	for id, cfg := range itemsConfig {
		out = append(out, CatalogItemOut{
			ID:               id,
			Name:             cfg.Name,
			Type:             cfg.Type,
			Nacenka:          getNacenka(id),
			Num:              cfg.Num,
			Effects:          cfg.Effects,
			ForbiddenEffects: cfg.ForbiddenEffects,
			MaxEffects:       cfg.MaxEffects,
			LoreMatch:        cfg.LoreMatch,
		})
	}
	return out
}

func initialPricePayload() PriceUpdate {
	mutex.RLock()
	defer mutex.RUnlock()
	return priceUpdatePayloadLocked()
}

func typesMapFromSlice(list []string) map[string]struct{} {
	types := make(map[string]struct{}, len(list))
	for _, t := range list {
		if t != "" {
			types[t] = struct{}{}
		}
	}
	return types
}

func typesMapKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func aggregateActiveTypesLocked() map[string]int {
	counts := make(map[string]int)
	for _, types := range clientActiveTypes {
		for t := range types {
			counts[t]++
		}
	}
	return counts
}

func setClientBotsPerType(ws *websocket.Conn, counts map[string]int) {
	if len(counts) == 0 {
		clientBotsPerType[ws] = make(map[string]int)
		return
	}
	clientBotsPerType[ws] = copyMap(counts)
}

func aggregateBotsPerTypeLocked() map[string]int {
	totals := make(map[string]int)
	for _, m := range clientBotsPerType {
		for t, n := range m {
			if n > 0 {
				totals[t] += n
			}
		}
	}
	return totals
}

func logGlobalFleetState(prefix string) {
	active := aggregateActiveTypesLocked()
	if len(active) == 0 {
		log.Printf("%s активных типов нет — adjustPrice для всех типов на паузе", prefix)
		return
	}
	parts := make([]string, 0, len(active))
	for t, n := range active {
		parts = append(parts, fmt.Sprintf("%s×%d", t, n))
	}
	sort.Strings(parts)
	log.Printf("%s активные типы: %s", prefix, strings.Join(parts, ", "))
}

func isMinecraftTypeActiveLocked(minecraftType string) bool {
	if minecraftType == "" {
		return false
	}
	for _, types := range clientActiveTypes {
		if _, ok := types[minecraftType]; ok {
			return true
		}
	}
	return false
}

func isMinecraftTypeActive(minecraftType string) bool {
	mutex.RLock()
	defer mutex.RUnlock()
	return isMinecraftTypeActiveLocked(minecraftType)
}

func setClientActiveTypes(ws *websocket.Conn, activeTypes []string) {
	clientActiveTypes[ws] = typesMapFromSlice(activeTypes)
}

func updateTypeFleetActivityLocked() {
	now := time.Now()
	active := make(map[string]struct{})
	for _, types := range clientActiveTypes {
		for t := range types {
			active[t] = struct{}{}
		}
	}
	for t := range active {
		lastTypePresence[t] = now
		if typeActiveSince[t].IsZero() {
			typeActiveSince[t] = now
		}
	}
	for t := range typeActiveSince {
		if _, ok := active[t]; !ok {
			delete(typeActiveSince, t)
			delete(lastTypePresence, t)
		}
	}
}

func typeWasActiveForAnalysisWindowLocked(minecraftType string, windowStart time.Time) bool {
	if !isMinecraftTypeActiveLocked(minecraftType) {
		return false
	}
	since, ok := typeActiveSince[minecraftType]
	if !ok || since.IsZero() {
		return false
	}
	return !since.After(windowStart)
}

func setClientFleetTypes(ws *websocket.Conn, fleetTypes []string) {
	clientFleetTypes[ws] = typesMapFromSlice(fleetTypes)
}

func logClientFleetDisconnect(ws *websocket.Conn) {
	fleet := typesMapKeys(clientFleetTypes[ws])
	active := typesMapKeys(clientActiveTypes[ws])
	log.Printf("[FLEET] оркестратор отключился | заявлено: %v | было активно: %v", fleet, active)
	delete(clientFleetTypes, ws)
	logGlobalFleetState("[FLEET] после отключения")
}

func publishPrices() {
	publishPriceUpdate()
}

func broadcastBroker() {
	for msg := range broadcast {
		mutex.Lock()
		clientsCopy := make([]*websocket.Conn, 0, len(clients))
		for client := range clients {
			clientsCopy = append(clientsCopy, client)
		}
		mutex.Unlock()

		for _, client := range clientsCopy {
			if err := client.WriteJSON(msg); err != nil {
				log.Printf("Ошибка при отправке через брокер: %v", err)
				mutex.Lock()
				delete(clients, client)
				delete(clientItems, client)
				delete(clientInventory, client)
				delete(clientActiveTypes, client)
				delete(clientFleetTypes, client)
				delete(clientBotsPerType, client)
				mutex.Unlock()
			}
		}
	}
}

func startCacheCleanup() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		jsonCacheMu.Lock()
		now := time.Now()
		for key, expiry := range jsonCache {
			if now.After(expiry) {
				delete(jsonCache, key)
			}
		}
		jsonCacheMu.Unlock()
	}
}

func getCurrentJsonList() []string {
	jsonCacheMu.RLock()
	defer jsonCacheMu.RUnlock()

	list := make([]string, 0, len(jsonCache))
	for k := range jsonCache {
		list = append(list, k)
	}
	return list
}

func pruneStaleDataKeys() {
	stale := make(map[string]struct{})
	for item := range data.Prices {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.Nacenkas {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.AdjustState {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.BuyStats {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.SellStats {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.TrySellStats {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.BuySum {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	for item := range data.SellSum {
		if _, ok := itemsConfig[item]; !ok {
			stale[item] = struct{}{}
		}
	}
	if len(stale) == 0 {
		return
	}
	ids := make([]string, 0, len(stale))
	for id := range stale {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		delete(data.Prices, id)
		delete(dailyData.Prices, id)
		delete(data.Nacenkas, id)
		delete(dailyData.Nacenkas, id)
		delete(data.AdjustState, id)
		delete(dailyData.AdjustState, id)
		delete(data.BuyStats, id)
		delete(dailyData.BuyStats, id)
		delete(data.SellStats, id)
		delete(dailyData.SellStats, id)
		delete(data.TrySellStats, id)
		delete(dailyData.TrySellStats, id)
		delete(data.BuySum, id)
		delete(dailyData.BuySum, id)
		delete(data.SellSum, id)
		delete(dailyData.SellSum, id)
	}
	log.Printf("[DATA] убраны устаревшие id (%d): %s", len(ids), strings.Join(ids, ", "))
}

func syncPriceMarkersFromConfig() {
	for item, cfg := range itemsConfig {
		p, ok := data.Prices[item]
		if !ok || p <= 0 {
			continue
		}
		want := cfg.BasePrice % 100
		got := p % 100
		if got == want {
			continue
		}
		fixed := (p/100)*100 + want
		log.Printf("[DATA] %s: маркер %d→%d (цена %d→%d)", item, got, want, p, fixed)
		data.Prices[item] = fixed
		dailyData.Prices[item] = fixed
	}
}

func loadDailyData(loc *time.Location) {
	mutex.Lock()
	defer mutex.Unlock()

	today := time.Now().In(loc).Format("2006-01-02")
	currentDay = today
	filename := fmt.Sprintf("data_%s.json", today)

	dailyData = DailyData{
		Date:         today,
		Prices:       make(map[string]int),
		Nacenkas:     make(map[string]int),
		AdjustState:  make(map[string]ItemAdjustState),
		BuyStats:     make(map[string]int),
		SellStats:    make(map[string]int),
		TrySellStats: make(map[string]int),
		BuySum:       make(map[string]int),
		SellSum:      make(map[string]int),
	}

	if file, err := os.ReadFile(filename); err == nil {
		if err := json.Unmarshal(file, &dailyData); err == nil && dailyData.Date == today {
			if dailyData.BuySum == nil {
				dailyData.BuySum = make(map[string]int)
			}
			if dailyData.SellSum == nil {
				dailyData.SellSum = make(map[string]int)
			}
			if dailyData.Nacenkas == nil {
				dailyData.Nacenkas = make(map[string]int)
			}
			if dailyData.AdjustState == nil {
				dailyData.AdjustState = make(map[string]ItemAdjustState)
			}
			for item, sum := range dailyData.BuySum {
				data.BuySum[item] = sum
			}
			for item, sum := range dailyData.SellSum {
				data.SellSum[item] = sum
			}
			for item, price := range dailyData.Prices {
				data.Prices[item] = price
			}
			for item, n := range dailyData.Nacenkas {
				data.Nacenkas[item] = n
			}
			for item, st := range dailyData.AdjustState {
				data.AdjustState[item] = st
			}
			for item, count := range dailyData.BuyStats {
				data.BuyStats[item] = count
			}
			for item, count := range dailyData.SellStats {
				data.SellStats[item] = count
			}
			for item, count := range dailyData.TrySellStats {
				data.TrySellStats[item] = count
			}
			log.Println("Данные успешно загружены из файла")
		}
	}

	pruneStaleDataKeys()
	syncPriceMarkersFromConfig()

	for item, cfg := range itemsConfig {
		if _, exists := data.Prices[item]; !exists {
			data.Prices[item] = cfg.BasePrice
			dailyData.Prices[item] = cfg.BasePrice
		}
	}

	ensureNacenkasInitialized()

	for item := range itemsConfig {
		swordTimes[item] = time.Now().Add(-itemsConfig[item].AnalysisTime)
	}

	snap := cloneDailySnapshotLocked()
	persistDailySnapshot(&snap)
}

func startItemTimers() {
	for item, cfg := range itemsConfig {
		go func(item string, cfg ItemConfig) {
			log.Printf("[TIMER] Запущен таймер для %s (интервал: %v)", item, cfg.AnalysisTime)
			time.Sleep(time.Duration(len(itemsConfig)-1) * time.Second)
			ticker := time.NewTicker(cfg.AnalysisTime)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					adjustAndReport(item, cfg)
				}
			}
		}(item, cfg)
	}
}

func getItemStatsForReporting(item string, since time.Time) (sales, buys, trySells, price int) {
	mutex.Lock()
	defer mutex.Unlock()

	sales = countRecentSales(item, since)
	buys = countRecentBuys(item, since)
	trySells = countRecentTrySells(item, since)
	price = data.Prices[item]
	return
}

func getInventoryStats(item string) (onHand, inInventory int) {
	mutex.Lock()
	defer mutex.Unlock()

	onHand = getItemCount(item)
	inInventory = getInventoryCount(item)

	return
}

func adjustAndReport(item string, cfg ItemConfig) {
	if !isMinecraftTypeActive(cfg.Type) {
		log.Printf("[SKIP] %s: тип %s — нет активных ботов на оркестраторах", item, cfg.Type)
		return
	}

	now := time.Now()
	start := now.Add(-cfg.AnalysisTime)

	sales, buys, trySells, price := getItemStatsForReporting(item, start)

	log.Printf("[ANALYSIS] %s: анализ с %s по %s. Продажи: %d (норма: %d)",
		item, start.Format("15:04:05"), now.Format("15:04:05"), sales, cfg.NormalSales)

	adjustPrice(item)

	newPrice := func() int {
		mutex.Lock()
		defer mutex.Unlock()
		return data.Prices[item]
	}()

	// Логируем в файл вместо Telegram
	onlineCount := getOnlineCount()
	onHand, inInventory := getInventoryStats(item)

	status := "OK"
	if sales < cfg.NormalSales {
		status = "LOW"
	}

	logLine := fmt.Sprintf(
		"[%s] %s | %s-%s | продажи: %d/%d | покупки: %d | try-sell: %d | цена: %d→%d | на руках: %d | онлайн: %d\n",
		time.Now().Format("15:04:05"),
		item, start.Format("15:04"), now.Format("15:04"),
		sales, cfg.NormalSales, buys, trySells,
		price, newPrice, onHand, onlineCount,
	)
	appendToFile("logs_interval.txt", logLine)
}

func getPriceChangeEmoji(oldPrice, newPrice int) string {
	if newPrice > oldPrice {
		return "📈 +"
	} else if newPrice < oldPrice {
		return "📉 -"
	}
	return "↔️ ="
}

func appendToFile(filename, content string) {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Ошибка открытия файла лога: %v", err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		log.Printf("Ошибка записи в файл лога: %v", err)
	}
}

func addPriceToHistory(item string, price int) {
	if price <= 0 {
		return
	}

	hist := priceHistory[item]
	if hist == nil {
		hist = &PriceHistory{
			Records: []PriceRecord{},
			Limit:   priceHistoryLimit,
		}
		priceHistory[item] = hist
	}

	hist.Records = append(hist.Records, PriceRecord{Price: price, Time: time.Now()})
	if len(hist.Records) > hist.Limit {
		hist.Records = hist.Records[1:]
	}

	log.Printf("[HISTORY] %s: добавлена цена %d (всего записей: %d)",
		item, price, len(hist.Records))
}

func getMinPriceFromHistory(item string) int {
	hist := priceHistory[item]
	if hist == nil || len(hist.Records) == 0 {
		return 0
	}

	min := hist.Records[0].Price
	for _, r := range hist.Records {
		if r.Price < min {
			min = r.Price
		}
	}
	return min
}

