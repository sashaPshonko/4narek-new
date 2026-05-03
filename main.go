package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
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

type PriceAndRatio struct {
	Prices map[string]int     `json:"prices"`
	Ratios map[string]float64 `json:"ratios"`
}

type PriceHistory struct {
    Prices []int `json:"prices"`
    Limit  int   `json:"limit"`
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
	clientItems     = make(map[*websocket.Conn]map[string]int)
	clientInventory = make(map[*websocket.Conn]map[string]int)
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
	BasePrice    int
	NormalSales  int
	NormalCount  int
	PriceStep    int
	AnalysisTime time.Duration
	MinPrice     int
	MaxPrice     int
	Type         string
}

type DailyData struct {
	Date         string             `json:"date"`
	Prices       map[string]int     `json:"prices"`
	Ratios       map[string]float64 `json:"ratios"`
	BuyStats     map[string]int     `json:"buy_stats"`
	SellStats    map[string]int     `json:"sell_stats"`
	TrySellStats map[string]int     `json:"try_sell_stats"`
	MessageID    int                `json:"message_id"`
	BuySum       map[string]int     `json:"buy_sum"`
    SellSum      map[string]int     `json:"sell_sum"`
	MinPrices    map[string]int     `json:"min_prices"`    // новая
    MaxPrices    map[string]int     `json:"max_prices"`
}

var (
	itemsConfig = map[string]ItemConfig{
		"pochti-megasword-1.21": {
			BasePrice:    3000002,
			NormalSales:  8,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_sword-1.21",
		},
		"sword7-1.21": {
			BasePrice:    1500003,
			NormalSales:  8,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_sword-1.21",
		},
		"megasword-1.21": {
			BasePrice:    4500004,
			NormalSales:  6,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_sword-1.21",
		},
		"штаны-1.21": {
			BasePrice:    1500005,
			NormalSales:  7,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_leggings-1.21",
		},
		// "штаны_позорные-1.21": {
		// 	BasePrice:    1200006,
		// 	NormalSales:  6,
		// 	PriceStep:    100000,
		// 	AnalysisTime: 10 * time.Minute,
		// 	Type:         "netherite_leggings-1.21",
		// },
		"нагрудник-1.21": {
			BasePrice:    1500007,
			NormalSales:  8,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_chestplate-1.21",
		},
		"ботинки-1.21": {
			BasePrice:    1500009,
			NormalSales:  8,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_boots-1.21",
		},
		// "ботинки_позорные-1.21": {
		// 	BasePrice:    1200010,
		// 	NormalSales:  5,
		// 	PriceStep:    100000,
		// 	AnalysisTime: 10 * time.Minute,
		// 	Type:         "netherite_boots-1.21",
		// },
		"шлем-1.21": {
			BasePrice:    1600011,
			NormalSales:  7,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_helmet-1.21",
		},
		// "шлем_позорный-1.21": {
		// 	BasePrice:    1200012,
		// 	NormalSales:  6,
		// 	PriceStep:    100000,
		// 	AnalysisTime: 10 * time.Minute,
		// 	Type:         "netherite_helmet-1.21",
		// }, //
		"фарм-1.21": {
			BasePrice:    1600013,
			NormalSales:  7,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "отдача-1.21",
		},// 
		// "pochti-diamond-1.21": {
		// 	BasePrice:    1600014,
		// 	NormalSales:  6,
		// 	PriceStep:    100000,
		// 	AnalysisTime: 10 * time.Minute,
		// 	Type:         "diamond-1.21",
		// },
		// "diamond-1.21": {
		// 	BasePrice:    3000015,
		// 	NormalSales:  6,
		// 	PriceStep:    100000,
		// 	AnalysisTime: 10 * time.Minute,
		// 	Type:         "diamond-1.21",
		// },
		"ботинки-крутые-1.21": {
			BasePrice:    3000016,
			NormalSales:  4,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_boots-1.21",
		}, //бульдозер-1-1.21
		"бульдозер-1-1.21": {
			BasePrice:    500017,
			NormalSales:  6,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_pickaxe-1.21",
		},
		"бульдозер-2-1.21": {
			BasePrice:    1200018,
			NormalSales:  6,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_pickaxe-1.21",
		},//кирка_крутая-1.21
		"кирка_крутая-1.21": {
			BasePrice:    1200019,
			NormalSales:  5,
			PriceStep:    100000,
			AnalysisTime: 10 * time.Minute,
			Type:         "netherite_pickaxe-1.21",
		},
	}
)

type TradeLog struct {
	Time time.Time
	Type string // "buy", "sell" или "try-sell"
}

type Data struct {
	Prices       map[string]int
	Ratios       map[string]float64
	BuyStats     map[string]int
	SellStats    map[string]int
	TrySellStats map[string]int
	LastTrade    map[string]time.Time
	TradeHistory map[string][]TradeLog
	BuySum       map[string]int   // новая
    SellSum      map[string]int   // новая
	MinPrices    map[string]int     // новая
    MaxPrices    map[string]int
	NeedPriceIncrease   map[string]bool 
	NeedPriceDecrease   map[string]bool 
	LastManualUpdate    map[string]time.Time
}

var (
	data    = &Data{}
	mutex   = sync.Mutex{}
	clients = make(map[*websocket.Conn]bool)

	currentDay string
	dailyData  DailyData

	swordTimes = make(map[string]time.Time)

	lastPriceUpdate = make(map[string]time.Time)

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

	// Инициализация бота Telegram
	b, err := bot.New(token)
	if err != nil {
		log.Printf("Error creating bot: %v", err)
		os.Exit(1)
	}
	tgBot = b

	// Инициализация данных
	data.Prices = make(map[string]int)
	data.BuyStats = make(map[string]int)
	data.SellStats = make(map[string]int)
	data.TrySellStats = make(map[string]int)
	data.LastTrade = make(map[string]time.Time)
	data.TradeHistory = make(map[string][]TradeLog)
	data.Ratios = make(map[string]float64)
	data.BuySum = make(map[string]int)
	data.SellSum = make(map[string]int)
	data.MinPrices = make(map[string]int)
	data.MaxPrices = make(map[string]int)
	data.NeedPriceIncrease = make(map[string]bool)
	data.NeedPriceDecrease = make(map[string]bool)
	data.LastManualUpdate = make(map[string]time.Time)

	for item, cfg := range itemsConfig {
    if _, exists := data.MinPrices[item]; !exists {
        data.MinPrices[item] = cfg.MinPrice
    }
    if _, exists := data.MaxPrices[item]; !exists {
        data.MaxPrices[item] = cfg.MaxPrice
    }
}

	// Загрузка данных за сегодня
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

func filterPrices() PriceAndRatio {
    filteredPrices := make(map[string]int)
    filteredRatios := make(map[string]float64)
    for k, v := range data.Prices {
        if _, ok := itemsConfig[k]; ok {
            filteredPrices[k] = v
        }
    }
    for k, v := range data.Ratios {
        if _, ok := itemsConfig[k]; ok {
            filteredRatios[k] = v
        }
    }
    return PriceAndRatio{Prices: filteredPrices, Ratios: filteredRatios}
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
	defer mutex.Unlock()

	today := time.Now().In(loc).Format("2006-01-02")
	currentDay = today
	filename := fmt.Sprintf("data_%s.json", today)

	dailyData = DailyData{
		Date:         today,
		Prices:       make(map[string]int),
		BuyStats:     make(map[string]int),
		SellStats:    make(map[string]int),
		TrySellStats: make(map[string]int),
		Ratios:       make(map[string]float64),
		BuySum:       make(map[string]int),   // добавить
    	SellSum:      make(map[string]int),   // добавить
		MinPrices:    make(map[string]int),   // добавить
        MaxPrices:    make(map[string]int),   // добавить
	}

	if file, err := os.ReadFile(filename); err == nil {
			if err := json.Unmarshal(file, &dailyData); err == nil && dailyData.Date == today {
				if dailyData.BuySum == nil {
				dailyData.BuySum = make(map[string]int)
			}
			if dailyData.SellSum == nil {
				dailyData.SellSum = make(map[string]int)
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
			for item, count := range dailyData.BuyStats {
				data.BuyStats[item] = count
			}
			for item, count := range dailyData.SellStats {
				data.SellStats[item] = count
			}
			for item, count := range dailyData.TrySellStats {
				data.TrySellStats[item] = count
			}
			for item, ratio := range dailyData.Ratios {
				data.Ratios[item] = ratio
			}
			 if dailyData.MinPrices == nil {
                dailyData.MinPrices = make(map[string]int)
            }
            if dailyData.MaxPrices == nil {
                dailyData.MaxPrices = make(map[string]int)
            }
            
            for item, price := range dailyData.MinPrices {
                data.MinPrices[item] = price
            }
            for item, price := range dailyData.MaxPrices {
                data.MaxPrices[item] = price
            }
			log.Println("Данные успешно загружены из файла")
		}
	}

	for item, cfg := range itemsConfig {
		if _, exists := data.Prices[item]; !exists {
			data.Prices[item] = cfg.BasePrice
			dailyData.Prices[item] = cfg.BasePrice
		}
		if _, exists := data.Ratios[item]; !exists {
			data.Ratios[item] = 0.8
			dailyData.Ratios[item] = 0.8
		}
	}

	for item, cfg := range itemsConfig {
        if _, exists := data.MinPrices[item]; !exists {
            data.MinPrices[item] = cfg.MinPrice
            dailyData.MinPrices[item] = cfg.MinPrice
        }
        if _, exists := data.MaxPrices[item]; !exists {
            data.MaxPrices[item] = cfg.MaxPrice
            dailyData.MaxPrices[item] = cfg.MaxPrice
        }
    }

	for item := range itemsConfig {
		swordTimes[item] = time.Now().Add(-itemsConfig[item].AnalysisTime)
	}

	saveDailyDataNoMessageUpdate()
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

func getItemStatsForReporting(item string, since time.Time) (sales, buys, trySells, price int, ratio float64) {
	mutex.Lock()
	defer mutex.Unlock()

	sales = countRecentSales(item, since)
	buys = countRecentBuys(item, since)
	trySells = countRecentTrySells(item, since)
	price = data.Prices[item]
	ratio = data.Ratios[item]

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
	now := time.Now()
	start := now.Add(-cfg.AnalysisTime)

	sales, buys, trySells, price, ratio := getItemStatsForReporting(item, start)

	log.Printf("[ANALYSIS] %s: анализ с %s по %s. Продажи: %d (норма: %d)",
		item, start.Format("15:04:05"), now.Format("15:04:05"), sales, cfg.NormalSales)

	adjustPrice(item)

	newPrice, newRatio := func() (int, float64) {
		mutex.Lock()
		defer mutex.Unlock()
		return data.Prices[item], data.Ratios[item]
	}()

	sendIntervalStatsToTelegram(
		item,
		start, now,
		float64(sales), float64(cfg.NormalSales), float64(buys), float64(trySells),
		float64(price), ratio, float64(newPrice), newRatio,
	)
}

func saveDailyDataNoMessageUpdate() {
	today := currentDay
	if today == "" {
		return
	}

	filename := fmt.Sprintf("data_%s.json", today)
	dailyData.Prices = data.Prices
	dailyData.BuyStats = data.BuyStats
	dailyData.SellStats = data.SellStats
	dailyData.TrySellStats = data.TrySellStats
	dailyData.Ratios = data.Ratios
	dailyData.BuySum = data.BuySum
	dailyData.SellSum = data.SellSum
	dailyData.MinPrices = data.MinPrices    // добавить
    dailyData.MaxPrices = data.MaxPrices 

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
		saveDailyDataNoMessageUpdate()
		mutex.Unlock()
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

		mutex.Lock()
		saveDailyData()
		mutex.Unlock()

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
	dailyData.BuyStats = data.BuyStats
	dailyData.SellStats = data.SellStats
	dailyData.TrySellStats = data.TrySellStats
	dailyData.Ratios = data.Ratios
	dailyData.BuySum = data.BuySum
	dailyData.SellSum = data.SellSum
	dailyData.MinPrices = data.MinPrices    // добавить
    dailyData.MaxPrices = data.MaxPrices    // добавить

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
	mutex.Unlock()

	defer func() {
		mutex.Lock()
		delete(clients, ws)
		delete(clientItems, ws)
		delete(clientInventory, ws)
		mutex.Unlock()
	}()

	// Отправляем начальные данные
	var jsonList []string

	mutex.Lock()
	jsonList = getCurrentJsonList()
	mutex.Unlock()

	select {
		case broadcast <- filterPrices():
		default:
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
			Action    string         `json:"action"`
			Type      string         `json:"type"`
			Items     map[string]int `json:"items"`
			Inventory map[string]int `json:"inventory"`
			Price int    `json:"price"`
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
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "buy"})
			data.BuySum[msg.Type] += msg.Price 
			addPriceToHistory(msg.Type, msg.Price)
			mutex.Unlock()

			mutex.Lock()
			saveDailyData()
			mutex.Unlock()

		case "sell":
			data.SellStats[msg.Type]++
			data.LastTrade[msg.Type] = time.Now()
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "sell"})
			data.SellSum[msg.Type] += msg.Price 
			mutex.Unlock()

			mutex.Lock()
			saveDailyData()
			mutex.Unlock()

		case "try-sell":
			data.TrySellStats[msg.Type]++
			data.LastTrade[msg.Type] = time.Now()
			data.TradeHistory[msg.Type] = append(data.TradeHistory[msg.Type], TradeLog{Time: time.Now(), Type: "try-sell"})
			mutex.Unlock()

			mutex.Lock()
			saveDailyData()
			mutex.Unlock()

		case "info":
			mutex.Unlock()

			select {
			case broadcast <- filterPrices():
			default:
			}

		case "presence":
			clientItems[ws] = copyMap(msg.Items)
			clientInventory[ws] = copyMap(msg.Inventory)
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

			mutex.Lock()
			saveDailyData()
			mutex.Unlock()

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
				log.Printf("[CONFIG] %s: цена уже %d, пропускаем", msg.Type, msg.Price)
				mutex.Unlock()
				continue
			}
			data.LastManualUpdate[msg.Type] = time.Now()
			
			// Сохраняем минимальную цену
			data.MinPrices[msg.Type] = msg.Price
			
			// 1. СРАЗУ МЕНЯЕМ ЦЕНУ
			oldPrice := data.Prices[msg.Type]
			data.Prices[msg.Type] = msg.Price
			
			// 2. СБРАСЫВАЕМ ФЛАГИ, чтобы плановое изменение ничего не делало
			data.NeedPriceIncrease[msg.Type] = false
			data.NeedPriceDecrease[msg.Type] = false
			
			// 3. СТАВИМ "ЗАМОРОЗКУ" - плановое изменение пропустит этот цикл
			lastPriceUpdate[msg.Type] = time.Now()
			
			log.Printf("[CONFIG] %s: цена мгновенно изменена %d -> %d", 
				msg.Type, oldPrice, msg.Price)
			
			// Отправляем всем клиентам
			pricesCopy := make(map[string]int)
			ratiosCopy := make(map[string]float64)
			for k, v := range data.Prices {
				pricesCopy[k] = v
			}
			for k, v := range data.Ratios {
				ratiosCopy[k] = v
			}
			mutex.Unlock()
			
			select {
			case broadcast <- PriceAndRatio{
				Prices: pricesCopy,
				Ratios: ratiosCopy,
			}:
			default:
			}
			
			// Сохраняем в файл
			mutex.Lock()
			saveDailyData()
			mutex.Unlock()

	case "set_max_price":
    if msg.Type == "" || msg.Price == 0 {
        mutex.Unlock()
        continue
    }
    
    if _, exists := itemsConfig[msg.Type]; !exists {
        mutex.Unlock()
        continue
    }

    if data.Prices[msg.Type] == msg.Price {
        log.Printf("[CONFIG] %s: цена уже %d, пропускаем", msg.Type, msg.Price)
        mutex.Unlock()
        continue
    }

    data.LastManualUpdate[msg.Type] = time.Now()
    
    // Сохраняем максимальную цену
    oldMaxPrice := data.MaxPrices[msg.Type]
    data.MaxPrices[msg.Type] = msg.Price
    
    // 1. СРАЗУ МЕНЯЕМ ЦЕНУ (только если она выше текущей)
    oldPrice := data.Prices[msg.Type]
    if msg.Price < oldPrice {  // только если максимальная цена ниже текущей
        data.Prices[msg.Type] = msg.Price
        
        // 2. СБРАСЫВАЕМ ФЛАГИ
        data.NeedPriceIncrease[msg.Type] = false
        data.NeedPriceDecrease[msg.Type] = false
        
        // 3. СТАВИМ "ЗАМОРОЗКУ"
        lastPriceUpdate[msg.Type] = time.Now()
        
        log.Printf("[CONFIG] %s: цена мгновенно понижена %d -> %d (макс: %d)", 
            msg.Type, oldPrice, msg.Price, msg.Price)
    } else {
        log.Printf("[CONFIG] %s: макс цена обновлена %d -> %d (текущая %d, изменение не требуется)", 
            msg.Type, oldMaxPrice, msg.Price, oldPrice)
    }
    
    // Отправляем всем клиентам
    pricesCopy := make(map[string]int)
    ratiosCopy := make(map[string]float64)
    for k, v := range data.Prices {
        pricesCopy[k] = v
    }
    for k, v := range data.Ratios {
        ratiosCopy[k] = v
    }
    mutex.Unlock()
    
    select {
    case broadcast <- PriceAndRatio{
        Prices: pricesCopy,
        Ratios: ratiosCopy,
    }:
    default:
    }
    
    // Сохраняем в файл
    mutex.Lock()
    saveDailyData()
    mutex.Unlock()

    // Отправляем подтверждение
    select {
    case broadcast <- map[string]interface{}{
        "action": "price_config_updated",
        "type":   msg.Type,
        "max_price": msg.Price,
    }:
    default:
    }
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

func downRatio(ratio float64) float64 {
	if ratio <= 0.75 {
		return 0
	}
	return ratio - 0.05
}

func upRatio(ratio float64) float64 {
	if ratio >= 0.85 {
		return 0
	}
	return ratio + 0.05
}
func adjustPrice(item string) {
	cfg, ok := itemsConfig[item]
	if !ok {
		return
	}

	mutex.Lock()
	now := time.Now()
	swordTimes[item] = now
	lastUpdate := now.Add(-cfg.AnalysisTime)

	if time.Since(data.LastManualUpdate[item]) < cfg.AnalysisTime {
		log.Printf("[SKIP] %s: ручное изменение %v назад, пропускаем анализ", 
			item, time.Since(data.LastManualUpdate[item]))
		mutex.Unlock()
		return
	}

	sales := countRecentSales(item, lastUpdate)
	// buys := countRecentBuys(item, lastUpdate)

	newPrice := data.Prices[item]
	priceBefore := newPrice
	ratioBefore := data.Ratios[item]

	// Используем сохраненные мин/макс цены (динамические!)
	minPrice := getMinPriceFromHistory(item)

	// --- 📊 СБОР СТАТИСТИКИ (как в старом добром коде) ---
	ahCounts := make(map[string]int)  // Только аукцион
	invCounts := make(map[string]int) // Только инвентарь
	
	for _, items := range clientItems {
		for name, count := range items {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				ahCounts[name] += count
			}
		}
	}
	for _, inv := range clientInventory {
		for name, count := range inv {
			if conf, exists := itemsConfig[name]; exists && conf.Type == cfg.Type {
				invCounts[name] += count
			}
		}
	}

	stockNorm := cfg.NormalSales
	if cfg.NormalCount > 0 {
		stockNorm = cfg.NormalCount
	}


	onAH := ahCounts[item]      // Сколько этого предмета на аукционе
	inInv := invCounts[item]    // Сколько этого предмета в инвентаре
	totalStock := onAH + inInv  // Общий запас (для лидера)

	// Определяем лидера типа (по общему количеству AH + INV)
	leaderID := ""
	maxTotal := -1
	for name := range itemsConfig {
		if itemsConfig[name].Type == cfg.Type {
			total := ahCounts[name] + invCounts[name]
			if total > maxTotal {
				maxTotal = total
				leaderID = name
			}
		}
	}

	// --- ⚖️ ЛОГИКА ЦЕНООБРАЗОВАНИЯ (ПОЛНОСТЬЮ ИЗ СТАРОГО КОДА) ---
	ratio := ratioBefore

	if data.NeedPriceDecrease[item] {
		targetPrice := data.MaxPrices[item]
		if targetPrice > 0 && targetPrice < newPrice {
			newPrice = targetPrice
			log.Printf("📉 Принудительное понижение %s по флагу: %d -> %d", 
				item, priceBefore, newPrice)
		}
		// Сбрасываем флаг
		data.NeedPriceDecrease[item] = false
	} else if data.NeedPriceIncrease[item] {
        targetPrice := data.MinPrices[item]
        if targetPrice > 0 && targetPrice > newPrice {
            newPrice = targetPrice
            log.Printf("🚀 Принудительное повышение %s по флагу: %d -> %d", 
                item, priceBefore, newPrice)
        }
        // Сбрасываем флаг
        data.NeedPriceIncrease[item] = false
    } else if sales < cfg.NormalSales && totalStock < stockNorm {
		newPrice += cfg.PriceStep
		log.Printf("📈 Повышение %s: мало товара (%d < %d)", item, totalStock, stockNorm)
	} else if (totalStock > sales && totalStock > stockNorm) && sales < cfg.NormalSales {
		if newPrice - cfg.PriceStep > minPrice + 200000 {
			newPrice -= cfg.PriceStep
		}
		log.Printf("📉 Снижение %s: плохие продажи (на АХ: %d, продажи: %d)", item, onAH, sales)
	}

	// --- ✅ ЗАВЕРШЕНИЕ ---
	if newPrice != priceBefore || ratio != ratioBefore {
		data.Prices[item] = newPrice
		dailyData.Prices[item] = newPrice
		data.Ratios[item] = ratio
		dailyData.Ratios[item] = ratio
		lastPriceUpdate[item] = now
		
		mutex.Unlock()

		log.Printf("[PRICE] %s: цена %d -> %d (На АХ: %d, Продажи: %d/%d, Лидер: %s)", 
			item, priceBefore, newPrice, onAH, sales, cfg.NormalSales, leaderID)

		select {
		case broadcast <- filterPrices():
		default:
		}
	} else {
		mutex.Unlock()
	}
}
func sendIntervalStatsToTelegram(item string, start, end time.Time, actualSales, expectedSales, buyCount, trySellCount,
    oldPrice, oldRatio, newPrice, newRatio float64) {

	// Мега-костыль: рандомная задержка от 3 до 7 секунд
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
            "🧮 Коэффициент: %.2f → %.2f\n"+
            "🎒 На аукционе: %d\n"+
            "🎒 В инвентаре: %d\n"+
            "👥 Онлайн: %d игроков",
        item,
        status,
        start.Format("15:04:05"),
        end.Format("15:04:05"),
        buyCount,
        trySellCount,
        actualSales,
        expectedSales,
        int(oldPrice), int(newPrice),
        getPriceChangeEmoji(int(oldPrice), int(newPrice)),
        oldRatio, newRatio,
        onHand,
        inInventory,
        onlineCount,
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
        "%s [%s → %s] %s | Покупки: %.0f | Продажи: %.0f/%.0f | Цена: %d→%d | Коэф: %.2f→%.2f | На руках: %d | Онлайн: %d\n",
        item,
        start.Format("15:04:05"),
        end.Format("15:04:05"),
        status,
        buyCount,
        actualSales,
        expectedSales,
        int(oldPrice), int(newPrice),
        oldRatio, newRatio,
        onHand,
        onlineCount,
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
        return // отрицательные цены не храним
    }
    
    hist := priceHistory[item]
    if hist == nil {
        hist = &PriceHistory{
            Prices: []int{},
            Limit:  priceHistoryLimit,
        }
        priceHistory[item] = hist
    }
    
    hist.Prices = append(hist.Prices, price)
    if len(hist.Prices) > hist.Limit {
        hist.Prices = hist.Prices[1:]
    }
    
    log.Printf("[HISTORY] %s: добавлена цена %d (всего записей: %d)", 
        item, price, len(hist.Prices))
}

// Получение минимальной цены из истории
func getMinPriceFromHistory(item string) int {
    hist := priceHistory[item]
    if hist == nil || len(hist.Prices) == 0 {
        return 0 // нет истории — не ограничиваем
    }
    
    min := hist.Prices[0]
    for _, p := range hist.Prices {
        if p < min {
            min = p
        }
    }
    return min
}


