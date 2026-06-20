package main

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func mlWSURL() string {
	if u := os.Getenv("ML_WS_URL"); u != "" {
		return u
	}
	return "ws://127.0.0.1:8090"
}

func mlShadowEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("ML_SHADOW")))
	return v == "1" || v == "true" || v == "yes"
}

func mlPredictTimeout() time.Duration {
	if ms := os.Getenv("ML_PREDICT_TIMEOUT_MS"); ms != "" {
		if d, err := time.ParseDuration(ms + "ms"); err == nil && d > 0 {
			return d
		}
	}
	return 4 * time.Second
}

// mlPredict — один запрос predict к Python sandbox (ws).
func mlPredict(ctx context.Context, payload map[string]any) (map[string]any, error) {
	u, err := url.Parse(mlWSURL())
	if err != nil {
		return nil, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(mlPredictTimeout()))

	// приветствие info
	if _, welcome, err := conn.ReadMessage(); err == nil {
		var info map[string]any
		if json.Unmarshal(welcome, &info) == nil {
			if info["ok"] == false {
				log.Printf("[ML-WS] server not ready: %v", info["error"])
			}
		}
	}

	req, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return nil, err
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
