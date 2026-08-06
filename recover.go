package main

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

func logPanic(name string, recovered any) {
	log.Printf("[PANIC] %s: %v\n%s", name, recovered, debug.Stack())
}

func runSafe(name string, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logPanic(name, recovered)
		}
	}()
	fn()
}

// goSafe — фоновая задача с recover (паника не убивает весь процесс).
func goSafe(name string, fn func()) {
	go func() {
		runSafe(name, fn)
	}()
}

// goImmortal запускает fn в фоне и перезапускает после panic или нормального выхода.
func goImmortal(name string, fn func()) {
	go func() {
		for {
			runSafe(name, fn)
			log.Printf("[RESTART] %s — повтор через 1s", name)
			time.Sleep(time.Second)
		}
	}()
}

func recoverHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logPanic("http:"+r.URL.Path, recovered)
			}
		}()
		next(w, r)
	}
}

func startHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", recoverHTTP(handleConnections))
	registerFunauthHTTP(mux)
	registerSalesHTTP(mux)
	registerFleetHTTP(mux)
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	for {
		log.Println("HTTP server listening on :8080")
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server stopped: %v", err)
		}
		time.Sleep(time.Second)
	}
}
