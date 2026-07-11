package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"golang.org/x/net/proxy"
)

const (
	telegramProxyDefault = "socks5h://127.0.0.1:1080"
	telegramChatID       = -4709535234
	telegramMinInterval  = 1200 * time.Millisecond // ~50 msg/min — ниже лимита TG
	telegramQueueSize    = 256
)

var tgBot *bot.Bot

type tgQueuedMsg struct {
	text      string
	parseMode string
}

var tgOutbox chan tgQueuedMsg

// experimentTelegramEvent — лог эксперимента в TG (после unlock adjustPrice).
type experimentTelegramEvent struct {
	Item           string
	Action         string
	PriceBefore    int
	PriceAfter     int
	NacenkaBefore  int
	NacenkaAfter   int
	NacenkaSumNow  int
	NacenkaSumPrev int
	Sales          int
}

func resolveTelegramProxyURL() string {
	v := strings.TrimSpace(os.Getenv("TELEGRAM_PROXY"))
	if v == "off" || v == "0" || v == "false" {
		return ""
	}
	if v != "" {
		return v
	}
	return telegramProxyDefault
}

func parseSocksHostPort(proxyURL string) (host, port string) {
	raw := strings.TrimSpace(proxyURL)
	for _, prefix := range []string{"socks5h://", "socks5://", "http://", "https://"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "127.0.0.1", "1080"
	}
	return host, port
}

func isTCPReachable(host, port string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func findXrayDir() string {
	if d := strings.TrimSpace(os.Getenv("XRAY_DIR")); d != "" {
		return d
	}
	candidates := []string{".", "../4narek-1.12"}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "..", "4narek-1.12"))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "xray.mjs")); err == nil {
			return dir
		}
	}
	return "."
}

func syncVlessToXrayDir(xrayDir string) {
	src := "vless.url"
	if _, err := os.Stat(src); err != nil {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	dst := filepath.Join(xrayDir, "vless.url")
	if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(data) {
		return
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		log.Printf("[Telegram] vless.url → %s: %v", dst, err)
		return
	}
	log.Printf("[Telegram] vless.url → %s", dst)
}

func telegramHTTPClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return &http.Client{Timeout: 60 * time.Second}
	}
	host, port := parseSocksHostPort(proxyURL)
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(host, port), nil, proxy.Direct)
	if err != nil {
		log.Printf("[Telegram] SOCKS5 dialer: %v", err)
		return &http.Client{Timeout: 60 * time.Second}
	}
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
	}
}

func isTelegramAPIOkViaProxy(proxyURL string) bool {
	client := telegramHTTPClient(proxyURL)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org", nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode > 0
}

func ensureTelegramXray() error {
	proxyURL := resolveTelegramProxyURL()
	if proxyURL == "" {
		log.Println("[Telegram] прокси выключен (TELEGRAM_PROXY=off)")
		return nil
	}

	host, port := parseSocksHostPort(proxyURL)
	if isTCPReachable(host, port, 2*time.Second) && isTelegramAPIOkViaProxy(proxyURL) {
		log.Printf("[Telegram] прокси OK: %s:%s", host, port)
		return nil
	}

	if os.Getenv("TELEGRAM_AUTO_XRAY") == "off" {
		return fmt.Errorf("SOCKS %s:%s недоступен (TELEGRAM_AUTO_XRAY=off)", host, port)
	}

	xrayDir := findXrayDir()
	syncVlessToXrayDir(xrayDir)
	log.Printf("[Telegram] запускаю xray: node xray.mjs (cwd=%s)", xrayDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "xray.mjs")
	cmd.Dir = xrayDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("[Telegram] xray: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("xray.mjs: %w", err)
	}

	for i := 0; i < 24; i++ {
		if isTCPReachable(host, port, 2*time.Second) && isTelegramAPIOkViaProxy(proxyURL) {
			log.Printf("[Telegram] xray поднял прокси %s:%s", host, port)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("таймаут ожидания SOCKS %s:%s", host, port)
}

func initTelegramBot() {
	if os.Getenv("TELEGRAM_DISABLED") == "1" {
		log.Println("[Telegram] отключён (TELEGRAM_DISABLED=1)")
		return
	}

	if err := ensureTelegramXray(); err != nil {
		log.Printf("[Telegram] xray: %v — отправка в TG недоступна", err)
		return
	}

	proxyURL := resolveTelegramProxyURL()
	httpClient := telegramHTTPClient(proxyURL)
	b, err := bot.New(token, bot.WithHTTPClient(60*time.Second, httpClient))
	if err != nil {
		log.Printf("[Telegram] bot.New: %v", err)
		return
	}
	tgBot = b
	startTelegramOutbox()
	log.Println("[Telegram] бот готов (api.telegram.org через xray)")
}

func startTelegramOutbox() {
	if tgOutbox != nil {
		return
	}
	tgOutbox = make(chan tgQueuedMsg, telegramQueueSize)
	go func() {
		for msg := range tgOutbox {
			sendTelegramNow(msg.text, msg.parseMode)
			time.Sleep(telegramMinInterval)
		}
	}()
	log.Printf("[Telegram] outbox: пауза %v между сообщениями", telegramMinInterval)
}

func sendTelegramNow(text, parseMode string) {
	if tgBot == nil || text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params := &bot.SendMessageParams{
		ChatID: telegramChatID,
		Text:   text,
	}
	if parseMode != "" {
		params.ParseMode = models.ParseMode(parseMode)
	}
	if _, err := tgBot.SendMessage(ctx, params); err != nil {
		log.Printf("[Telegram] send: %v", err)
	}
}

/** Поставить сообщение в очередь (не блокирует adjustPrice). */
func enqueueTelegramMessage(text, parseMode string) {
	if tgBot == nil || text == "" {
		return
	}
	if tgOutbox == nil {
		startTelegramOutbox()
	}
	select {
	case tgOutbox <- tgQueuedMsg{text: text, parseMode: parseMode}:
	default:
		log.Printf("[Telegram] очередь полна — дроп (%d символов)", len(text))
	}
}

func enqueueExperimentTelegram(ev experimentTelegramEvent) {
	var title, body string
	switch ev.Action {
	case "experiment_start":
		title = "🧪 Эксперимент СТАРТ"
		body = "3 цикла роста Σнаценок → +цена и +наценка. Ждём следующий цикл."
	case "experiment_ok":
		title = "🧪 Эксперимент OK"
		body = "Σнаценок продаж не упала — оставляем цену/наценку."
	case "experiment_rollback":
		title = "🧪 Эксперимент FAIL"
		body = "Σнаценок продаж упала — откат цены/наценки."
	default:
		title = "🧪 Эксперимент"
		body = ev.Action
	}
	msg := fmt.Sprintf(
		"*%s*\n"+
			"📦 `%s`\n"+
			"%s\n"+
			"💰 Цена: %d → %d\n"+
			"🏷 Наценка: %d → %d\n"+
			"Σ наценок продаж: *%d* (было %d)\n"+
			"📊 Продаж в окне: %d",
		title,
		ev.Item,
		body,
		ev.PriceBefore, ev.PriceAfter,
		ev.NacenkaBefore, ev.NacenkaAfter,
		ev.NacenkaSumNow, ev.NacenkaSumPrev,
		ev.Sales,
	)
	enqueueTelegramMessage(msg, "Markdown")
}

func enqueueBuySurgeTelegram(ev BuySurgeEvent) {
	if !ev.Dropped {
		return
	}
	msg := fmt.Sprintf(
		"⚡ *Buy surge* — цена сразу вниз\n"+
			"📦 `%s`\n"+
			"💬 surge-счётчик ≥2× нормы и продаж < нормы (цикл не трогаем)\n"+
			"⚡ Surge: *%d* / %d → сброс в 0\n"+
			"📊 Продажи в окне: *%d* из *%d*\n"+
			"💰 Цена: %d → %d (−%d)",
		ev.Item,
		ev.SurgeCount, ev.Threshold,
		ev.Sales, ev.NormalSales,
		ev.PriceBefore, ev.PriceAfter, ev.Step,
	)
	enqueueTelegramMessage(msg, "Markdown")
}

// fetchAndLogTelegramChats — getUpdates: id чатов, куда бот получал сообщения.
func fetchAndLogTelegramChats() error {
	if err := ensureTelegramXray(); err != nil {
		return err
	}
	client := telegramHTTPClient(resolveTelegramProxyURL())
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100", token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	var payload struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message *struct {
				Chat struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
					Type  string `json:"type"`
				} `json:"chat"`
				Text string `json:"text"`
			} `json:"message"`
			ChannelPost *struct {
				Chat struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
					Type  string `json:"type"`
				} `json:"chat"`
			} `json:"channel_post"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if !payload.OK {
		return fmt.Errorf("getUpdates: %s", string(body))
	}

	seen := make(map[int64]string)
	for _, u := range payload.Result {
		if u.Message != nil {
			c := u.Message.Chat
			title := c.Title
			if title == "" {
				title = strings.TrimSpace(u.Message.Text)
			}
			seen[c.ID] = fmt.Sprintf("%s (%s)", title, c.Type)
		}
		if u.ChannelPost != nil {
			c := u.ChannelPost.Chat
			seen[c.ID] = fmt.Sprintf("%s (channel)", c.Title)
		}
	}
	if len(seen) == 0 {
		log.Println("[Telegram] чатов в getUpdates нет — напиши что-нибудь в группу с ботом и повтори")
		return nil
	}
	log.Println("[Telegram] chat id из getUpdates:")
	for id, label := range seen {
		log.Printf("  %d  %s", id, label)
	}
	return nil
}

func runListTelegramChats() {
	if err := fetchAndLogTelegramChats(); err != nil {
		log.Fatalf("[Telegram] %v", err)
	}
}

func getInventoryStats(item string) (onHand, inInventory int) {
	mutex.RLock()
	defer mutex.RUnlock()
	return getItemCount(item), getInventoryCount(item)
}

func getPriceChangeEmoji(oldPrice, newPrice int) string {
	if newPrice > oldPrice {
		return "📈 +"
	}
	if newPrice < oldPrice {
		return "📉 -"
	}
	return "↔️ ="
}

func appendToFile(filename, content string) {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[Telegram] log file %s: %v", filename, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		log.Printf("[Telegram] log write %s: %v", filename, err)
	}
}

func sendIntervalStatsToTelegram(
	item string,
	start, end time.Time,
	onlineCount int,
	rep AdjustReport,
) {
	if tgBot == nil {
		return
	}

	status := "✅"
	if rep.Sales < rep.NormalSales {
		status = "⚠️"
	}
	if rep.Skipped {
		status = "⏸"
	}

	priceEmoji := getPriceChangeEmoji(rep.PriceBefore, rep.PriceAfter)
	nacenkaLine := fmt.Sprintf("%d", rep.NacenkaAfter)
	if rep.NacenkaBefore != rep.NacenkaAfter {
		nacenkaLine = fmt.Sprintf("%d → %d", rep.NacenkaBefore, rep.NacenkaAfter)
	}

	decisionMark := "↔️"
	switch {
	case rep.Skipped:
		decisionMark = "⏸"
	case rep.PriceAfter > rep.PriceBefore || rep.NacenkaAfter > rep.NacenkaBefore:
		decisionMark = "📈"
	case rep.PriceAfter < rep.PriceBefore || rep.NacenkaAfter < rep.NacenkaBefore:
		decisionMark = "📉"
	}

	msg := fmt.Sprintf(
		"*%s* %s\n"+
			"⏳ Интервал: %s - %s\n"+
			"📦 Покупки: *%d*\n"+
			"🛒 Попытки продаж: *%d*\n"+
			"📊 Продажи: *%d* из *%d* (норма)\n"+
			"💰 Цена: %d → %d (%s)\n"+
			"🏷 Наценка: %s\n"+
			"🎒 АХ: %d | Инв: %d | Всего: %d\n"+
			"📐 Доля слотов: %d (свободно %d, нужно %d)\n"+
			"🧱 Пол цены: %d | step: %d\n"+
			"Σ наценок продаж: %d (прошлый цикл %d) | streak %d/3 | cd %d\n"+
			"👥 Онлайн: %d\n"+
			"\n"+
			"%s *Решение:* `%s`\n"+
			"💬 %s",
		item,
		status,
		start.Format("15:04:05"),
		end.Format("15:04:05"),
		rep.Buys,
		rep.TrySells,
		rep.Sales,
		rep.NormalSales,
		rep.PriceBefore, rep.PriceAfter,
		priceEmoji,
		nacenkaLine,
		rep.OnAH, rep.Inv, rep.Held,
		rep.Share, rep.Free, rep.Need,
		rep.PriceFloor, rep.Step,
		rep.NacenkaSumNow, rep.NacenkaSumPrev, rep.GoodStreak, rep.Cooldown,
		onlineCount,
		decisionMark,
		rep.Action,
		rep.Reason,
	)
	if rep.NoOverstockDown || rep.BlockNacenkaUp {
		msg += "\n🛡 Защита: buys<sales и есть место"
		if rep.NoOverstockDown {
			msg += " — не роняем sell из‑за стока"
		}
		if rep.BlockNacenkaUp {
			msg += " — наценку не поднимаем"
		}
	}

	enqueueTelegramMessage(msg, "Markdown")

	plainLog := fmt.Sprintf(
		"%s [%s → %s] %s %s | buys=%d try=%d sales=%d/%d | price %d→%d nac %d→%d | AH=%d inv=%d held=%d share=%d | %s | %s\n",
		item,
		start.Format("15:04:05"),
		end.Format("15:04:05"),
		status,
		rep.Action,
		rep.Buys, rep.TrySells, rep.Sales, rep.NormalSales,
		rep.PriceBefore, rep.PriceAfter, rep.NacenkaBefore, rep.NacenkaAfter,
		rep.OnAH, rep.Inv, rep.Held, rep.Share,
		rep.Reason,
		decisionMark,
	)
	appendToFile("logs_interval.txt", plainLog)
}

func updateTelegramMessageWithoutLocks(prices, buyStats, sellStats map[string]int, date string, messageID int) {
	if tgBot == nil {
		return
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var newMessageID int
	if messageID == 0 {
		msg, err := tgBot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    telegramChatID,
			Text:   msgText,
		})
		if err != nil {
			log.Printf("[Telegram] дашборд send: %v", err)
			return
		}
		newMessageID = msg.ID
	} else {
		_, err := tgBot.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    telegramChatID,
			MessageID: messageID,
			Text:      msgText,
		})
		if err != nil {
			log.Printf("[Telegram] дашборд edit: %v", err)
			msg, sendErr := tgBot.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:    telegramChatID,
				Text:   msgText,
			})
			if sendErr != nil {
				log.Printf("[Telegram] дашборд resend: %v", sendErr)
				return
			}
			newMessageID = msg.ID
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
	mutex.RLock()
	prices := maps.Clone(data.Prices)
	buyStats := maps.Clone(data.BuyStats)
	sellStats := maps.Clone(data.SellStats)
	date := dailyData.Date
	messageID := dailyData.MessageID
	mutex.RUnlock()
	updateTelegramMessageWithoutLocks(prices, buyStats, sellStats, date, messageID)
}
