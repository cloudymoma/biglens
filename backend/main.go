package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	cfg, err := LoadConfig("conf.yaml")
	if err != nil {
		cfg, err = LoadConfig("../conf.yaml")
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}
	}

	logDir := "logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, 0755)
	}

	serverLogger := slog.New(slog.NewJSONHandler(&lumberjack.Logger{
		Filename:   fmt.Sprintf("%s/server.log", logDir),
		MaxSize:    32,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}, nil))
	slog.SetDefault(serverLogger)

	accessLogger := slog.New(slog.NewJSONHandler(&lumberjack.Logger{
		Filename:   fmt.Sprintf("%s/access.log", logDir),
		MaxSize:    32,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}, nil))

	ctx := context.Background()
	bq, err := NewBQClient(ctx, cfg)
	if err != nil {
		slog.Error("failed to initialize BigQuery", "error", err)
		os.Exit(1)
	}

	api := NewAPIHandler(bq)

	mux := http.NewServeMux()

	logMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			accessLogger.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("ip", r.RemoteAddr),
			)
		})
	}

	h := func(f http.HandlerFunc) http.Handler { return logMW(f) }

	// Dashboard aggregate endpoints (errgroup concurrency + caching)
	mux.Handle("/api/dashboard/storage", h(api.StorageDashboard))
	mux.Handle("/api/dashboard/compute", h(api.ComputeDashboard))
	mux.Handle("/api/dashboard/cost", h(api.CostDashboard))
	mux.Handle("/api/dashboard/insights", h(api.InsightsDashboard))

	// IAM Security endpoints
	mux.Handle("/api/dashboard/iam", h(api.IAMDashboard))
	mux.Handle("/api/iam/emails", h(api.SearchEmails))

	// Dataplex / Knowledge Catalog (OKF bundle) endpoints
	mux.Handle("/api/catalog/graph", h(api.CatalogGraph))
	mux.Handle("/api/catalog/search", h(api.CatalogSearch))
	mux.Handle("/api/catalog/concept", h(api.CatalogConcept))
	mux.Handle("/api/catalog/types", h(api.CatalogTypes))
	mux.Handle("/api/catalog/import", h(api.CatalogImport))

	// Individual endpoints
	mux.Handle("/api/storage", h(api.StorageStats))
	mux.Handle("/api/slots", h(api.SlotUsage))
	mux.Handle("/api/config", h(api.Config))
	mux.Handle("/api/datasets", h(api.ListDatasets))
	mux.Handle("/api/tables", h(api.ListTables))
	mux.Handle("/api/regions", h(api.ListRegions))

	// Static files
	staticDir := "backend/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		staticDir = "static"
	}
	mux.Handle("/", logMW(http.FileServer(http.Dir(staticDir))))

	port := cfg.Server.Port
	if port == 0 {
		port = 1983
	}

	slog.Info("Starting BigLens", "port", port, "mode", cfg.Server.Mode)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %s\n", err)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
