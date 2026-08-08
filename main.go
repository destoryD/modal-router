package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"modals-router/internal/api"
	"modals-router/internal/balancer"
	"modals-router/internal/modalauth"
	"modals-router/internal/proxy"
	"modals-router/internal/store"
)

//go:embed web
var webFS embed.FS

type Config struct {
	Listen     string
	DataDir    string
	MaxRetries int
	AdminToken string
}

func loadConfig() Config {
	return Config{
		Listen:     getenv("ROUTER_LISTEN", ":8080"),
		DataDir:    getenv("ROUTER_DATA_DIR", "./data"),
		MaxRetries: getenvInt("ROUTER_MAX_RETRIES", 3),
		AdminToken: os.Getenv("ROUTER_ADMIN_TOKEN"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	cfg := loadConfig()

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	dataFile := filepath.Join(cfg.DataDir, "channels.json")
	s, err := store.New(dataFile)
	if err != nil {
		log.Fatalf("failed to create store: %v", err)
	}

	b := balancer.New()
	b.Reload(s.ListChannels())

	p := proxy.New(s, b, cfg.MaxRetries)
	a := api.New(s, b, p, cfg.AdminToken)

	modalStore, err := modalauth.NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("failed to create modal auth store: %v", err)
	}
	modalHandler := modalauth.NewHandler(modalStore)

	s.StartFlusher(30 * time.Second)
	go autoReenableLoop(s, b)

	mux := http.NewServeMux()

	mux.Handle("/admin/api/", http.StripPrefix("/admin/api", a.Handler()))
	mux.Handle("/admin/modal/", http.StripPrefix("/admin/modal", modalHandler.Routes()))

	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to create web sub FS: %v", err)
	}
	mux.Handle("/admin/", http.StripPrefix("/admin", http.FileServer(http.FS(webSub))))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == "GET" {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		p.ServeHTTP(w, r)
	})

	log.Printf("modals-router listening on %s", cfg.Listen)
	log.Printf("data file: %s", dataFile)
	log.Printf("admin UI: http://localhost%s/admin/", cfg.Listen)
	if cfg.AdminToken != "" {
		log.Printf("admin token auth enabled")
	}

	server := &http.Server{
		Addr:         cfg.Listen,
		Handler:      mux,
		ReadTimeout:  0,
		WriteTimeout: 0,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		s.Flush()
		server.Close()
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func autoReenableLoop(s *store.Store, b *balancer.Balancer) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.AutoReenable()
		b.Reload(s.ListChannels())
	}
}
