package main

import (
	"context"
	"embed"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"rss_reader/db"
	"rss_reader/feed"
	"rss_reader/server"
)

//go:embed static
var staticFiles embed.FS

func main() {
	addr    := flag.String("addr", ":8080", "listen address")
	ttl     := flag.Duration("ttl", 15*time.Minute, "feed cache TTL")
	logFile := flag.String("log", "", "path to log file (empty to disable)")
	flag.Parse()

	// Load .env — silently ignored in production where vars are set directly.
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using environment variables")
	}

	// LOG_FILE env overrides the flag; flag default is empty (disabled).
	if *logFile == "" {
		if envLog := os.Getenv("LOG_FILE"); envLog != "" {
			*logFile = envLog
		}
	}

	// Configure structured logger.
	// Writes to stdout always; also writes to a log file when -log is set.
	// LOG_FORMAT=json  → JSON output (good for production log shippers).
	// LOG_LEVEL        → debug | info | warn | error  (default: info)
	logLevel := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn", "warning":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}

	opts   := &slog.HandlerOptions{Level: logLevel}
	out    := io.Writer(os.Stdout)

	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			slog.Error("opening log file", "path", *logFile, "err", err)
			os.Exit(1)
		}
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
		slog.Info("logging to file", "path", *logFile)
	}

	var handler slog.Handler
	if os.Getenv("LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	slog.SetDefault(slog.New(handler))

	slog.Info("starting RSS Reader", "addr", *addr, "cache_ttl", ttl.String())

	// Connect to Postgres.
	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		slog.Error("connecting to database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("connected to database")

	store := db.NewStore(pool)
	cache := feed.NewCache(*ttl)

	srv := server.New(store, cache)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Serve embedded static files.
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		slog.Error("static fs", "err", err)
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	slog.Info("listening", "addr", "http://localhost"+*addr)
	if err := http.ListenAndServe(*addr, server.RequestLogger(mux)); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
