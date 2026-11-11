package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		p := u.Port()
		if p == "" {
			p = "1080"
		}
		return u.Hostname(), p
	}
	for _, prefix := range []string{"socks5h://", "socks5://", "http://", "https://"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		raw = raw[i+1:]
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

func socks5ContextDialer(proxyURL string) (proxy.ContextDialer, error) {
	raw := strings.TrimSpace(proxyURL)
	if raw == "" {
		return nil, fmt.Errorf("empty socks url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" {
		host, port = parseSocksHostPort(raw)
	}
	if port == "" {
		port = "1080"
	}
	var auth *proxy.Auth
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		if user != "" {
			auth = &proxy.Auth{User: user, Password: pass}
		}
	}
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort(host, port), auth, proxy.Direct)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks dialer without ContextDialer")
	}
	return cd, nil
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

func startTelegramOutbox() {
	if tgOutbox != nil {
		return
	}
	tgOutbox = make(chan tgQueuedMsg, telegramQueueSize)
	goSafe("telegram:outbox", func() {
		for msg := range tgOutbox {
			runSafe("telegram:send", func() {
				sendTelegramNow(msg.text, msg.parseMode)
			})
			time.Sleep(telegramMinInterval)
		}
	})
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
