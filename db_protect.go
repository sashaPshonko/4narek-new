package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SQLite DSN: WAL + FULL sync + длинный busy_timeout.
// FULL чуть медленнее NORMAL, но сильно снижает риск порчи при kill -9 / power loss.
func mlOpenDSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(15000)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=temp_store(MEMORY)"
}

func mlBackupDir() string {
	if v := os.Getenv("ML_BACKUP_DIR"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(mlDBPath), "backups")
}

func mlBackupKeep() int {
	if v := os.Getenv("ML_BACKUP_KEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 28
}

func mlBackupInterval() time.Duration {
	hours := 6
	if v := os.Getenv("ML_BACKUP_INTERVAL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	return time.Duration(hours) * time.Hour
}

// backupMLDatabase — консистентный снимок через VACUUM INTO (безопасно при живом WAL).
// Не копировать pricing.db руками со -wal/-shm: получите битый файл.
func backupMLDatabase() error {
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	if mlDB == nil {
		return fmt.Errorf("db not open")
	}
	dir := mlBackupDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(dir, "pricing-"+stamp+".db")
	q := fmt.Sprintf(`VACUUM INTO '%s'`, strings.ReplaceAll(dest, `'`, `''`))
	if _, err := mlDB.Exec(q); err != nil {
		return err
	}
	log.Printf("[ML] backup → %s", dest)
	rotateBackups(dir, "pricing-", ".db", mlBackupKeep())
	return nil
}

func rotateBackups(dir, prefix, suffix string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) <= keep {
		return
	}
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

func startMLBackupLoop() {
	interval := mlBackupInterval()
	goImmortal("mlBackup", func() {
		// первый бэкап через 2 минуты после старта (не блокировать init)
		time.Sleep(2 * time.Minute)
		for {
			if err := backupMLDatabase(); err != nil {
				log.Printf("[ML] backup: %v", err)
			}
			time.Sleep(interval)
		}
	})
	log.Printf("[ML] backups every %s → %s (keep %d)", interval, mlBackupDir(), mlBackupKeep())
}

func closeMLLog() {
	mlDBMu.Lock()
	defer mlDBMu.Unlock()
	if mlDB == nil {
		return
	}
	_, _ = mlDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := mlDB.Close(); err != nil {
		log.Printf("[ML] close: %v", err)
	} else {
		log.Printf("[ML] closed cleanly (checkpoint)")
	}
	mlDB = nil
}

// setupMLShutdown — SIGINT/SIGTERM: checkpoint WAL в основной файл и закрытие.
// kill -9 всё равно опасен — поэтому ротация бэкапов важнее.
func setupMLShutdown() {
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		log.Printf("[shutdown] %v — closing pricing.db", sig)
		closeMLLog()
		os.Exit(0)
	}()
}
