package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/gorilla/websocket"
)

const (
	token    = "7209712528:AAF7o20ysTcpgQb8JlVH4_CLmqH_iz5GiL8"
	chatID   = -4709535234
	timezone = "Asia/Tashkent"
)

type PriceUpdate struct {
	Prices  map[string]int   `json:"prices"`
	Catalog []CatalogItemOut `json:"catalog,omitempty"`
}

type CatalogItemOut struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Type    string       `json:"type"`
	Nacenka int          `json:"nacenka"`
	Num     int          `json:"num"`
	Effects   []ItemEffect `json:"effects"`
	LoreMatch string       `json:"lore_match,omitempty"`
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
	tgBot *bot.Bot
)

var (
	clientItems       = make(map[*websocket.Conn]map[string]int)
	clientInventory   = make(map[*websocket.Conn]map[string]int)
	clientActiveTypes = make(map[*websocket.Conn]map[string]struct{})
	clientFleetTypes  = make(map[*websocket.Conn]map[string]struct{})
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
	ID           string
	Name         string
	Type         string
	BasePrice    int
	NormalSales  int
	NormalCount  int
	PriceStep    int
	AnalysisTime time.Duration
	Nacenka      int
	NacenkaMin   int
	Num          int
	Effects      []ItemEffect
	LoreMatch    string
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
	Type  string // "buy", "sell" или "try-sell"
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
	// mutex: Lock — запись (data, clients, adjustPrice). RLock — только чтение (publishPriceUpdate).
	// Нельзя: Lock и в той же горутине RLock/publishPrices — дедлок RWMutex.
	mutex   = sync.RWMutex{}
	clients = make(map[*websocket.Conn]bool)

	currentDay string
	dailyData  DailyData

	swordTimes = make(map[string]time.Time)

	lastPriceUpdate = make(map[string]time.Time)

	// Когда go-тип впервые появился в active_types; сбрасывается, если ботов типа нет.
	typeActiveSince  = make(map[string]time.Time)
	lastTypePresence = make(map[string]time.Time)

	// Новый: канал рассылки
	broadcast = make(chan interface{}, 1000)

	// Кэш для json_data
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

	b, err := bot.New(token)
	if err != nil {
		log.Printf("Error creating bot: %v", err)
		os.Exit(1)
	}
	tgBot = b

	// Инициализация данных
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

	// Запускаем брокер рассылки
	go broadcastBroker()

	// Запускаем очистку кэша
	go startCacheCleanup()

	// WebSocket сервер
	http.HandleFunc("/ws", handleConnections)
	go func() {
		log.Println("Server started on :8080")
		log.Print(http.ListenAndServe(":8080", nil))
	}()

	// Проверка смены дня
	go checkDayChange(loc)

	// Запускаем таймеры для каждого предмета
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
			ID:        id,
			Name:      cfg.Name,
			Type:      cfg.Type,
			Nacenka:   getNacenka(id),
			Num:       cfg.Num,
			Effects:   cfg.Effects,
			LoreMatch: cfg.LoreMatch,
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

// isMinecraftTypeActiveLocked — вызывать только под mutex.Lock (не RLock!).
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

// Тип активен, если хотя бы один подключённый оркестратор прислал живых ботов этого типа.
func isMinecraftTypeActive(minecraftType string) bool {
	mutex.RLock()
	defer mutex.RUnlock()
	return isMinecraftTypeActiveLocked(minecraftType)
}

func setClientActiveTypes(ws *websocket.Conn, activeTypes []string) {
	clientActiveTypes[ws] = typesMapFromSlice(activeTypes)
}

// updateTypeFleetActivityLocked — после presence: фиксируем, с какого момента тип в флоте онлайн.
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

// typeWasActiveForAnalysisWindowLocked — тип был активен с начала окна анализа (бот не перезапускался mid-window).
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

// publishPrices — только без удержанного mutex.Lock (внутри RLock).
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

func getConnectedClientsCount() int {
	mutex.Lock()
	defer mutex.Unlock()
	return len(clients)
}

func loadDailyData(loc *time.Location) {
	mutex.Lock()

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
	mutex.Unlock()
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

	sendIntervalStatsToTelegram(
		item, start, now,
		float64(sales), float64(cfg.NormalSales), float64(buys), float64(trySells),
		price, newPrice,
	)
}

// cloneDailySnapshotLocked — снимок dailyData; вызывать только под mutex.Lock.
func cloneDailySnapshotLocked() DailyData {
	dailyData.Prices = data.Prices
	dailyData.Nacenkas = data.Nacenkas
	dailyData.AdjustState = data.AdjustState
	dailyData.BuyStats = data.BuyStats
	dailyData.SellStats = data.SellStats
	dailyData.TrySellStats = data.TrySellStats
	dailyData.BuySum = data.BuySum
	dailyData.SellSum = data.SellSum

	return DailyData{
		Date:         currentDay,
		MessageID:    dailyData.MessageID,
		Prices:       maps.Clone(dailyData.Prices),
		Nacenkas:     maps.Clone(dailyData.Nacenkas),
		AdjustState:  maps.Clone(dailyData.AdjustState),
		BuyStats:     maps.Clone(dailyData.BuyStats),
		SellStats:    maps.Clone(dailyData.SellStats),
		TrySellStats: maps.Clone(dailyData.TrySellStats),
		BuySum:       maps.Clone(dailyData.BuySum),
		SellSum:      maps.Clone(dailyData.SellSum),
	}
}

func persistDailySnapshot(snap *DailyData) {
	if snap == nil || snap.Date == "" {
		return
	}
	filename := fmt.Sprintf("data_%s.json", snap.Date)
	file, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("Ошибка сохранения данных: %v", err)
		return
	}
	if err := os.WriteFile(filename, file, 0644); err != nil {
		log.Printf("Ошибка записи файла: %v", err)
	}
}

func saveDailyDataNoMessageUpdate() {
	mutex.Lock()
	snap := cloneDailySnapshotLocked()
	mutex.Unlock()
	persistDailySnapshot(&snap)
}

func updateTelegramMessageWithoutLocks(prices, buyStats, sellStats map[string]int, date string, messageID int) {
	currentTime := time.Now().Format("2006-01-02 15:04:05")

	msgText := fmt.Sprintf("📊 Статистика за %s\nПоследнее обновление: %s\n\n", date, currentTime)

	for item := range itemsConfig {
		msgText += fmt.Sprintf(
			"🔹 %s: %d ₽\n🛒 Куплено: %d\n💰 Продано: %d\n\n",
			item,
			prices[item],
			buyStats[item],
			sellStats[item],
		)
	}

	ctx := context.Background()

	var newMessageID int
	if messageID == 0 {
		msg, err := tgBot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   msgText,
		})
		if err != nil {
			log.Printf("[Telegram error] Не удалось отправить новое сообщение: %v", err)
			return
		}
		newMessageID = msg.ID
	} else {
		_, err := tgBot.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: messageID,
			Text:      msgText,
		})
		if err != nil {
			log.Printf("[Telegram error] Не удалось обновить сообщение: %v", err)

			msg, sendErr := tgBot.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   msgText,
			})
			if sendErr == nil {
				newMessageID = msg.ID
			} else {
				log.Printf("[Telegram error] Повторная отправка тоже не удалась: %v", sendErr)
				return
			}
		}
	}

	if newMessageID != 0 {
		mutex.Lock()
		dailyData.MessageID = newMessageID
		snap := cloneDailySnapshotLocked()
		mutex.Unlock()
		persistDailySnapshot(&snap)
	}
}

func updateTelegramMessageSimple() {
	mutex.Lock()
	prices := make(map[string]int)
	buyStats := make(map[string]int)
	sellStats := make(map[string]int)
	date := dailyData.Date
	messageID := dailyData.MessageID

	for k, v := range data.Prices {
		prices[k] = v
	}
	for k, v := range data.BuyStats {
		buyStats[k] = v
	}
	for k, v := range data.SellStats {
		sellStats[k] = v
	}
	mutex.Unlock()

	updateTelegramMessageWithoutLocks(prices, buyStats, sellStats, date, messageID)
}

func checkDayChange(loc *time.Location) {
	for {
		now := time.Now().In(loc)
		nextDay := now.Add(24 * time.Hour)
		nextDay = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), 0, 0, 0, 0, loc)
		time.Sleep(time.Until(nextDay))

		saveDailyDataNoMessageUpdate()

		loadDailyData(loc)
	}
}

func saveDailyData() {
	today := currentDay
	if today == "" {
		return
	}

	filename := fmt.Sprintf("data_%s.json", today)
	dailyData.Prices = data.Prices
	dailyData.Nacenkas = data.Nacenkas
	dailyData.AdjustState = data.AdjustState
	dailyData.BuyStats = data.BuyStats
	dailyData.SellStats = data.SellStats
	dailyData.TrySellStats = data.TrySellStats
	dailyData.BuySum = data.BuySum
	dailyData.SellSum = data.SellSum

	file, err := json.MarshalIndent(dailyData, "", "  ")
	if err != nil {
		log.Printf("Ошибка сохранения данных: %v", err)
		return
	}

	if err := os.WriteFile(filename, file, 0644); err != nil {
		log.Printf("Ошибка записи файла: %v", err)
		return
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print(err, " upgrade error")
		return
	}
	defer ws.Close()

	mutex.Lock()
	clients[ws] = true
	clientItems[ws] = make(map[string]int)
	clientInventory[ws] = make(map[string]int)
	clientActiveTypes[ws] = make(map[string]struct{})
	clientFleetTypes[ws] = make(map[string]struct{})
	mutex.Unlock()

	defer func() {
		mutex.Lock()
		delete(clients, ws)
		delete(clientItems, ws)
		delete(clientInventory, ws)
		delete(clientActiveTypes, ws)
		logClientFleetDisconnect(ws)
		mutex.Unlock()
	}()

	// Отправляем начальные данные
	jsonList := getCurrentJsonList()

	if err := ws.WriteJSON(initialPricePayload()); err != nil {
		log.Printf("ошибка отправки каталога: %v", err)
	}

	select {
	case broadcast <- map[string]interface{}{
		"action": "json_update",
		"data":   jsonList,
	}:
	default:
	}

	for {
		_, rawMsg, err := ws.ReadMessage()
		if err != nil {
			log.Printf("read error: %v", err)
			break
		}

		var msg struct {
			Action      string         `json:"action"`
			Type        string         `json:"type"`
			Items       map[string]int `json:"items"`
			Inventory   map[string]int `json:"inventory"`
			Types       []string       `json:"types"`
			ActiveTypes []string       `json:"active_types"`
			Price       int            `json:"price"`
			Floors         map[string]int `json:"floors"`
			WindowStartMs  int64          `json:"window_start_ms"`
			WindowEndMs    int64          `json:"window_end_ms"`
			WindowMs       int64          `json:"window_ms"`
		}
		if msg.Action != "add" {
			log.Printf("[WS incoming] %s", string(rawMsg))
		}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			log.Printf("json unmarshal error: %v", err)
			continue
		}

		mutex.Lock()
		switch msg.Action {
		case "buy":
			data.BuyStats[msg.Type]++
			data.LastTrade[msg.Type] = time.Now()
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "buy", Price: msg.Price})
			data.BuySum[msg.Type] += msg.Price
			addPriceToHistory(msg.Type, msg.Price)
			mutex.Unlock()
			saveDailyDataNoMessageUpdate()

		case "sell":
			data.SellStats[msg.Type]++
			data.LastTrade[msg.Type] = time.Now()
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "sell", Price: msg.Price})
			data.SellSum[msg.Type] += msg.Price
			mutex.Unlock()
			saveDailyDataNoMessageUpdate()

		case "try-sell":
			data.TrySellStats[msg.Type]++
			data.LastTrade[msg.Type] = time.Now()
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "try-sell"})
			mutex.Unlock()
			saveDailyDataNoMessageUpdate()

		case "ah_market_floor":
			floors := msg.Floors
			windowStart := msg.WindowStartMs
			windowEnd := msg.WindowEndMs
			windowMs := msg.WindowMs
			mutex.Unlock()
			applyMarketFloors(floors, windowStart, windowEnd, windowMs)

		case "info":
			mutex.Unlock()
			if err := ws.WriteJSON(initialPricePayload()); err != nil {
				log.Printf("ошибка info payload: %v", err)
			}

		case "fleet":
			fleetTypes := msg.Types
			if len(fleetTypes) == 0 {
				fleetTypes = msg.ActiveTypes
			}
			setClientFleetTypes(ws, fleetTypes)
			log.Printf("[FLEET] оркестратор подключился | заявленные типы: %v", typesMapKeys(clientFleetTypes[ws]))
			mutex.Unlock()
			logGlobalFleetState("[FLEET]")

		case "presence":
			clientItems[ws] = copyMap(msg.Items)
			clientInventory[ws] = copyMap(msg.Inventory)
			setClientActiveTypes(ws, msg.ActiveTypes)
			updateTypeFleetActivityLocked()
			mutex.Unlock()

		case "add":
			jsonData, exists := rawJSONField(rawMsg, "json_data")
			if !exists || jsonData == "" {
				mutex.Unlock()
				continue
			}

			jsonCacheMu.Lock()
			jsonCache[jsonData] = time.Now().Add(jsonCacheTTL)
			jsonCacheMu.Unlock()

			updatedList := getCurrentJsonList()

			mutex.Unlock()

			select {
			case broadcast <- map[string]interface{}{
				"action": "json_update",
				"data":   updatedList,
			}:
			default:
				log.Println("Буфер broadcast переполнен при отправке json_update")
			}

			saveDailyDataNoMessageUpdate()

		case "set_min_price":
			if msg.Type == "" || msg.Price == 0 {
				mutex.Unlock()
				continue
			}
			if _, exists := itemsConfig[msg.Type]; !exists {
				mutex.Unlock()
				continue
			}
			if data.Prices[msg.Type] == msg.Price {
				mutex.Unlock()
				continue
			}
			oldPrice := data.Prices[msg.Type]
			data.Prices[msg.Type] = msg.Price
			data.LastManualUpdate[msg.Type] = time.Now()
			log.Printf("[CONFIG] %s: min -> цена %d -> %d", msg.Type, oldPrice, msg.Price)
			mutex.Unlock()
			publishPrices()
			saveDailyDataNoMessageUpdate()

		case "set_max_price":
			if msg.Type == "" || msg.Price == 0 {
				mutex.Unlock()
				continue
			}
			if _, exists := itemsConfig[msg.Type]; !exists {
				mutex.Unlock()
				continue
			}
			oldPrice := data.Prices[msg.Type]
			if msg.Price >= oldPrice {
				mutex.Unlock()
				continue
			}
			data.Prices[msg.Type] = msg.Price
			data.LastManualUpdate[msg.Type] = time.Now()
			log.Printf("[CONFIG] %s: max -> цена %d -> %d", msg.Type, oldPrice, msg.Price)
			mutex.Unlock()
			publishPrices()
			saveDailyDataNoMessageUpdate()
		default:
			mutex.Unlock()
		}
	}
}

func rawJSONField(data []byte, field string) (string, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", false
	}
	if val, ok := m[field]; ok {
		if s, ok := val.(string); ok {
			return s, true
		}
	}
	return "", false
}

func copyMap(m map[string]int) map[string]int {
	cp := make(map[string]int)
	for k, v := range m {
		if v > 0 {
			cp[k] = v
		}
	}
	return cp
}

func countRecentSales(item string, since time.Time) int {
	count := 0
	for _, trade := range data.TradeHistory[item] {
		if trade.Type == "sell" && trade.Time.After(since) {
			count++
		}
	}
	return count
}

func getItemCount(item string) int {
	count := 0
	for _, items := range clientItems {
		count += items[item]
	}
	return count
}

func getInventoryCount(item string) int {
	count := 0
	for _, items := range clientInventory {
		count += items[item]
	}
	return count
}

func getInventoryFreeSlots(itemType string) int {
	count := 0
	for _, items := range clientInventory {
		for t, c := range items {
			if itemsConfig[t].Type == itemType {
				count += c
			}
		}
	}
	return inventoryLimit[itemType] - count
}

func countRecentBuys(item string, since time.Time) int {
	count := 0
	for _, trade := range data.TradeHistory[item] {
		if trade.Type == "buy" && trade.Time.After(since) {
			count++
		}
	}
	return count
}

func countRecentTrySells(item string, since time.Time) int {
	count := 0
	for _, trade := range data.TradeHistory[item] {
		if trade.Type == "try-sell" && trade.Time.After(since) {
			count++
		}
	}
	return count
}

func sendIntervalStatsToTelegram(item string, start, end time.Time, actualSales, expectedSales, buyCount, trySellCount float64,
	oldPrice, newPrice int) {

	time.Sleep(time.Duration(rand.Intn(4000)+3000) * time.Millisecond)

	status := "✅"
	if actualSales < expectedSales {
		status = "⚠️"
	}

	onlineCount := getOnlineCount()
	onHand, inInventory := getInventoryStats(item)

	msg := fmt.Sprintf(
		"*%s* %s\n"+
			"⏳ Интервал: %s - %s\n"+
			"📦 Покупки: *%.0f*\n"+
			"🛒 Попытки продаж: *%.0f*\n"+
			"📊 Продажи: *%.0f* из *%.0f* (норма)\n"+
			"💰 Цена: %d → %d (%s)\n"+
			"🎒 На аукционе: %d\n"+
			"🎒 В инвентаре: %d\n"+
			"👥 Онлайн: %d игроков",
		item, status,
		start.Format("15:04:05"), end.Format("15:04:05"),
		buyCount, trySellCount,
		actualSales, expectedSales,
		oldPrice, newPrice,
		getPriceChangeEmoji(oldPrice, newPrice),
		onHand, inInventory, onlineCount,
	)

    ctx := context.Background()
    _, err := tgBot.SendMessage(ctx, &bot.SendMessageParams{
        ChatID:    -4633184325,
        Text:      msg,
        ParseMode: "Markdown",
    })
    if err != nil {
        log.Printf("[Telegram] Ошибка при отправке интервал-статы: %v", err)
    }

	plainLog := fmt.Sprintf(
		"%s [%s → %s] %s | Покупки: %.0f | Продажи: %.0f/%.0f | Цена: %d→%d | На руках: %d | Онлайн: %d\n",
		item,
		start.Format("15:04:05"), end.Format("15:04:05"),
		status, buyCount, actualSales, expectedSales,
		oldPrice, newPrice, onHand, onlineCount,
	)

    appendToFile("logs_interval.txt", plainLog)
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

func getOnlineCount() int {
	resp, err := http.Get("http://45.141.76.110:5000/status")
	if err != nil {
		log.Printf("Ошибка запроса онлайна: %v", err)
		return -1
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Ошибка чтения тела ответа: %v", err)
		return -1
	}

	var status struct {
		PlayersOnline int `json:"players_online"`
	}

	if err := json.Unmarshal(body, &status); err != nil {
		log.Printf("Ошибка парсинга JSON онлайна: %v", err)
		return -1
	}

	return status.PlayersOnline
}

// Добавление цены покупки в историю
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

// Получение минимальной цены из истории покупок
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


