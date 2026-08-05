package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
)

var (
	funauthPoolInst   *funauthPool
	funauthBinderInst *funauthBinder
	funauthInitOnce   sync.Once

	// nick → last bind outcome: "pending" | "ok" | "no_accounts" | "fail"
	funauthNickState   = make(map[string]string)
	funauthNickStateMu sync.Mutex
)

func initFunauth() {
	funauthInitOnce.Do(func() {
		funauthPoolInst = newFunauthPool()
		funauthBinderInst = newFunauthBinder(funauthPoolInst)
		funauthPoolInst.init()
		log.Printf("[funauth] native MTProto ready (UI /funauth/)")
	})
}

func registerFunauthHTTP(mux *http.ServeMux) {
	initFunauth()

	mux.HandleFunc("/funauth", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/funauth/", http.StatusFound)
	}))

	staticRoot, err := fs.Sub(funauthStaticFS, "funauth_static")
	if err != nil {
		log.Printf("[funauth] embed static: %v", err)
	} else {
		fileServer := http.FileServer(http.FS(staticRoot))
		mux.Handle("/funauth/", recoverHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/funauth/api/") {
				funauthAPI(w, r)
				return
			}
			// Strip /funauth prefix for static files.
			r2 := *r
			u := *r.URL
			u.Path = strings.TrimPrefix(r.URL.Path, "/funauth")
			if u.Path == "" {
				u.Path = "/"
			}
			r2.URL = &u
			fileServer.ServeHTTP(w, &r2)
		})))
	}

	mux.HandleFunc("/api/funauth/bind", recoverHTTP(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body funauthBindReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		handleFunauthBindWS(body.Nick, body.Password)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"queued":true}`))
	}))
}

func recoverHTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logPanic("http:"+r.URL.Path, recovered)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func funauthAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/funauth/api")
	if path == "" {
		path = "/"
	}

	switch {
	case path == "/health" && r.Method == http.MethodGet:
		funauthJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"accounts": len(funauthPoolInst.list()),
			"ready":    funauthPoolInst.readyCount(),
			"queue":    funauthBinderInst.queueLen(),
			"configured": funauthPoolInst.configured(),
		})
		return

	case path == "/accounts" && r.Method == http.MethodGet:
		if !funauthPoolInst.configured() {
			funauthJSONErr(w, http.StatusServiceUnavailable, "funauth not ready")
			return
		}
		funauthJSON(w, http.StatusOK, funauthPoolInst.list())
		return

	case path == "/queue" && r.Method == http.MethodGet:
		funauthJSON(w, http.StatusOK, funauthBinderInst.status())
		return

	case path == "/login/start" && r.Method == http.MethodPost:
		funauthLoginStart(w, r)
		return

	case path == "/login/code" && r.Method == http.MethodPost:
		funauthLoginCode(w, r)
		return

	case path == "/login/password" && r.Method == http.MethodPost:
		funauthLoginPassword(w, r)
		return

	case path == "/login/authkey" && r.Method == http.MethodPost:
		funauthLoginAuthKey(w, r)
		return

	case path == "/bind" && r.Method == http.MethodPost:
		var body funauthBindReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			funauthJSONErr(w, http.StatusBadRequest, "bad json")
			return
		}
		handleFunauthBindWS(body.Nick, body.Password)
		funauthJSON(w, http.StatusAccepted, map[string]any{"ok": true, "queued": true})
		return

	case strings.HasPrefix(path, "/accounts/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, "/accounts/")
		id = strings.Trim(id, "/")
		if id == "" {
			funauthJSONErr(w, http.StatusBadRequest, "id_required")
			return
		}
		if !funauthPoolInst.configured() {
			funauthJSONErr(w, http.StatusServiceUnavailable, "funauth not ready")
			return
		}
		_ = funauthPoolInst.remove(id)
		funauthJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	funauthJSONErr(w, http.StatusNotFound, "not_found")
}

func funauthLoginStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		funauthJSONErr(w, http.StatusBadRequest, "bad json")
		return
	}
	result, err := funauthPoolInst.loginStart(body.Phone)
	if err != nil {
		funauthWritePoolErr(w, err)
		return
	}
	out := map[string]any{"ok": true}
	for k, v := range result {
		out[k] = v
	}
	funauthJSON(w, http.StatusOK, out)
}

func funauthLoginCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		funauthJSONErr(w, http.StatusBadRequest, "bad json")
		return
	}
	result, err := funauthPoolInst.loginCode(body.Phone, body.Code)
	if err != nil {
		funauthWritePoolErr(w, err)
		return
	}
	out := map[string]any{"ok": true}
	for k, v := range result {
		out[k] = v
	}
	funauthJSON(w, http.StatusOK, out)
}

func funauthLoginPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		funauthJSONErr(w, http.StatusBadRequest, "bad json")
		return
	}
	result, err := funauthPoolInst.loginPassword(body.Phone, body.Password)
	if err != nil {
		funauthWritePoolErr(w, err)
		return
	}
	out := map[string]any{"ok": true}
	for k, v := range result {
		out[k] = v
	}
	funauthJSON(w, http.StatusOK, out)
}

func funauthLoginAuthKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthKey string `json:"auth_key"`
		Session string `json:"session"`
		DCID    int    `json:"dc_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil {
		funauthJSONErr(w, http.StatusBadRequest, "bad json")
		return
	}
	raw := strings.TrimSpace(body.AuthKey)
	if raw == "" {
		raw = strings.TrimSpace(body.Session)
	}
	view, err := funauthPoolInst.importAuthKey(raw, body.DCID)
	if err != nil {
		funauthWritePoolErr(w, err)
		return
	}
	funauthJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       view.ID,
		"phone":    view.Phone,
		"username": view.Username,
		"ready":    view.Ready,
		"full":     view.Full,
		"started":  view.Started,
	})
}

func funauthWritePoolErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, errFunauthNotConfigured):
		status = http.StatusServiceUnavailable
		msg = "funauth not ready"
	case strings.Contains(msg, "required") ||
		strings.Contains(msg, "not_started") ||
		strings.Contains(msg, "authkey_") ||
		strings.Contains(msg, "telethon_session") ||
		strings.Contains(msg, "dc_invalid"):
		status = http.StatusBadRequest
	}
	funauthJSONErr(w, status, msg)
}

type funauthBindReq struct {
	Nick     string `json:"nick"`
	Password string `json:"password"`
}

func funauthNickKey(nick string) string {
	return strings.ToLower(strings.TrimSpace(nick))
}

// Повтор: всегда если очередь binder пуста; иначе — только новый nick / после no_accounts.
// Пока этот nick уже pending и очередь не пуста — не дублируем.
func funauthMayStartBind(nick string) (ok bool, reason string) {
	idle := funauthBinderInst != nil && funauthBinderInst.queueLen() == 0
	key := funauthNickKey(nick)
	funauthNickStateMu.Lock()
	defer funauthNickStateMu.Unlock()
	st, exists := funauthNickState[key]
	if st == "pending" && !idle {
		return false, "already_pending"
	}
	if idle || !exists || st == "no_accounts" {
		funauthNickState[key] = "pending"
		return true, ""
	}
	return false, "skip:" + st
}

func funauthSetNickState(nick, state string) {
	key := funauthNickKey(nick)
	funauthNickStateMu.Lock()
	funauthNickState[key] = state
	funauthNickStateMu.Unlock()
}

// handleFunauthBindWS — очередь bind; дубликаты nick режем, пока binder занят.
// Если очередь пуста — принимаем снова (уже привязан → ошибка от бота, ок).
func handleFunauthBindWS(nick, password string) {
	initFunauth()
	nick = strings.TrimSpace(nick)
	password = strings.TrimSpace(password)
	if nick == "" || password == "" {
		log.Printf("[funauth] skip empty nick/password")
		return
	}
	if ok, why := funauthMayStartBind(nick); !ok {
		log.Printf("[funauth] skip %s (%s)", nick, why)
		return
	}

	goSafe("funauth:bind:"+nick, func() {
		log.Printf("[funauth] bind start %s", nick)

		if funauthPoolInst == nil || !funauthPoolInst.configured() {
			log.Printf("[funauth] not ready for %s", nick)
			funauthSetNickState(nick, "no_accounts") // чтобы можно было повторить после починки
			enqueueTelegramMessage(
				fmt.Sprintf("⚠️ FunAuth недоступен для `%s`", nick),
				"Markdown",
			)
			broadcastFunauthResult(map[string]interface{}{
				"action": "funauth_result",
				"ok":     false,
				"nick":   nick,
				"error":  "funauth_not_configured",
			})
			return
		}

		result := funauthBinderInst.Bind(nick, password)

		if result.Error == "no_accounts" {
			funauthSetNickState(nick, "no_accounts")
			msg := fmt.Sprintf(
				"🚨 FunAuth: нет свободных TG-аккаунтов для `%s`\nДобавь акк: http://127.0.0.1:8080/funauth/",
				nick,
			)
			enqueueTelegramMessage(msg, "Markdown")
			broadcastFunauthResult(map[string]interface{}{
				"action": "funauth_no_accounts",
				"ok":     false,
				"nick":   nick,
				"error":  "no_accounts",
			})
			return
		}

		if result.OK {
			funauthSetNickState(nick, "ok")
			log.Printf("[funauth] ok %s via %s", nick, result.TgPhone)
			enqueueTelegramMessage(
				fmt.Sprintf("✅ FunAuth: `%s` привязан (tg %s), 2FA выкл", nick, result.TgPhone),
				"Markdown",
			)
		} else {
			// Не no_accounts — повтор с флота не делаем (только руками / сброс state)
			funauthSetNickState(nick, "fail:"+result.Error)
			log.Printf("[funauth] fail %s: %s", nick, result.Error)
			enqueueTelegramMessage(
				fmt.Sprintf("❌ FunAuth fail `%s`: %s", nick, result.Error),
				"Markdown",
			)
		}
		broadcastFunauthResult(map[string]interface{}{
			"action":  "funauth_result",
			"ok":      result.OK,
			"nick":    nick,
			"tgPhone": result.TgPhone,
			"error":   result.Error,
		})
	})
}

func broadcastFunauthResult(payload map[string]interface{}) {
	select {
	case broadcast <- payload:
	default:
		log.Println("[funauth] broadcast buffer full")
	}
}

func funauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func funauthJSONErr(w http.ResponseWriter, status int, msg string) {
	funauthJSON(w, status, map[string]any{"ok": false, "error": msg})
}
