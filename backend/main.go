package main

import (
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	backfill := flag.Bool("address-risk-backfill", false, "backfill USDT/USDC freeze history from BigQuery, then exit")
	since := flag.String("since", "", "backfill from this date, YYYY-MM-DD (default 2017-11-28: full history)")
	sinceDays := flag.Int("since-days", 0, "backfill the last N days (dev only; excludes --since)")
	yes := flag.Bool("yes", false, "run the backfill; without it only a dry-run estimate is printed")
	flag.Parse()

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

	if *backfill {
		os.Exit(runBackfillCLI(ctx, cfg, bqStablecoinSource{client: bq.client}, *since, *sinceDays, *yes))
	}

	applyCryptoGasConfig(cfg.CryptoGas)
	api := NewAPIHandler(bq)

	res, err := NewResClients(ctx, cfg)
	if err != nil {
		slog.Error("failed to initialize GCP resource clients", "error", err)
		os.Exit(1)
	}
	api.res = res

	// Address Risk (Crypto Pulse). A store failure must not stop the server:
	// lookups then report local lists as unavailable and still run live checks.
	var rstore *riskStore
	invalidate := func() { api.cache.Delete(riskOverviewCacheKey) }
	if s, err := openRiskStore(cfg.AddressRisk.dbPath()); err != nil {
		slog.Error("address risk store unavailable", "path", cfg.AddressRisk.dbPath(), "error", err)
	} else {
		rstore = s
		lists := newRiskListSyncer(rstore)
		lists.onChange = invalidate
		stable := &stablecoinSyncer{store: rstore, src: bqStablecoinSource{client: bq.client}, now: time.Now,
			initialDays: cfg.AddressRisk.initialSyncDays(), onChange: invalidate}
		go runAddressRiskSync(ctx, time.Hour, lists.syncDue, stable.syncOnce)
	}
	api.risk = newAddressRiskService(rstore, cfg.AddressRisk.rpcURLs())
	api.risk.cfg = cfg
	api.risk.bqSrc = bqStablecoinSource{client: bq.client}
	api.risk.invalidateCache = invalidate
	api.risk.setEtherscanKey(cfg.AddressRisk.EtherscanAPIKey)

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

	gzipMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			w.Header().Del("Content-Length")
			gz := gzip.NewWriter(w)
			defer gz.Close()
			next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
		})
	}

	h := func(f http.HandlerFunc) http.Handler { return logMW(gzipMW(f)) }

	// Dashboard aggregate endpoints (errgroup concurrency + caching)
	mux.Handle("/api/dashboard/storage", h(api.StorageDashboard))
	mux.Handle("/api/dashboard/compute", h(api.ComputeDashboard))
	mux.Handle("/api/dashboard/cost", h(api.CostDashboard))
	mux.Handle("/api/dashboard/insights", h(api.InsightsDashboard))
	mux.Handle("/api/dashboard/jobs", h(api.JobsDashboard))

	// IAM Security endpoints
	mux.Handle("/api/dashboard/iam", h(api.IAMDashboard))
	mux.Handle("/api/iam/emails", h(api.SearchEmails))
	mux.Handle("/api/dashboard/security", h(api.SecurityDashboard))

	// Dataplex / Knowledge Catalog (OKF bundle) endpoints
	mux.Handle("/api/catalog/graph", h(api.CatalogGraph))
	mux.Handle("/api/catalog/search", h(api.CatalogSearch))
	mux.Handle("/api/catalog/concept", h(api.CatalogConcept))
	mux.Handle("/api/catalog/types", h(api.CatalogTypes))
	mux.Handle("/api/catalog/import", h(api.CatalogImport))
	mux.Handle("/api/catalog/manifest", h(api.CatalogManifest))

	// BigQuery Open Data endpoints (public datasets; namespaced per dataset)
	mux.Handle("/api/opendata/trends/meta", h(api.TrendsMetaHandler))
	mux.Handle("/api/opendata/trends/dashboard", h(api.TrendsDashboard))
	mux.Handle("/api/opendata/trends/term", h(api.TrendsTerm))
	mux.Handle("/api/opendata/sem/meta", h(api.SemMeta))
	mux.Handle("/api/opendata/sem/dashboard", h(api.SemDashboard))
	mux.Handle("/api/opendata/sem/geo", h(api.SemGeo))
	mux.Handle("/api/opendata/sem/pulse", h(api.SemPulse))
	mux.Handle("/api/opendata/sem/term", h(api.SemTerm))
	mux.Handle("/api/opendata/sem/safety", h(api.SemSafety))
	mux.Handle("/api/opendata/gdelt/events", h(api.GdeltEvents))
	mux.Handle("/api/opendata/gdelt/gkg", h(api.GdeltGkg))
	mux.Handle("/api/opendata/gdelt/dyads", h(api.GdeltDyads))
	mux.Handle("/api/opendata/gdelt/country", h(api.GdeltCountry))
	mux.Handle("/api/opendata/gdelt/impact", h(api.GdeltImpact))
	mux.Handle("/api/opendata/gdelt/stories", h(api.GdeltStories))
	mux.Handle("/api/opendata/gdelt/industry", h(api.GdeltIndustry))
	mux.Handle("/api/opendata/weather/meta", h(api.WeatherMeta))
	mux.Handle("/api/opendata/weather/dashboard", h(api.WeatherDashboard))
	mux.Handle("/api/opendata/crypto/pulse", h(api.CryptoPulse))
	mux.Handle("/api/opendata/crypto/fees", h(api.CryptoFees))
	mux.Handle("/api/opendata/crypto/whales", h(api.CryptoWhales))
	mux.Handle("/api/opendata/crypto/tokens", h(api.CryptoTokens))
	mux.Handle("/api/opendata/crypto/mining", h(api.CryptoMining))
	mux.Handle("/api/opendata/crypto/spot", h(api.CryptoSpot))
	mux.Handle("/api/opendata/crypto/gas-pulse", h(api.CryptoGasPulse))
	mux.Handle("/api/opendata/crypto/gas-live", h(api.CryptoGasLive))
	mux.Handle("/api/opendata/crypto/address-risk/lookup", h(api.AddressRiskLookup))
	mux.Handle("/api/opendata/crypto/address-risk/sources", h(api.AddressRiskSources))
	mux.Handle("/api/opendata/crypto/address-risk/overview", h(api.AddressRiskOverview))
	mux.Handle("/api/opendata/crypto/address-risk/keys", logMW(addressRiskKeysHandler(api)))
	mux.Handle("/api/opendata/crypto/address-risk/backfill", logMW(addressRiskBackfillHandler(api)))
	mux.Handle("/api/gcp_billing/config", h(api.BillingConfig))
	mux.Handle("/api/gcp_billing/meta", h(api.BillingMeta))
	mux.Handle("/api/gcp_billing/overview", h(api.BillingOverview))
	mux.Handle("/api/gcp_billing/services", h(api.BillingServices))
	mux.Handle("/api/gcp_billing/projects", h(api.BillingProjects))
	mux.Handle("/api/gcp_billing/credits", h(api.BillingCredits))
	mux.Handle("/api/gcp_billing/resources", h(api.BillingResources))
	mux.Handle("/api/gcp_billing/pricing", h(api.BillingPricing))

	// GCP Resources endpoints (live inventory; conf.yaml lists the projects)
	mux.Handle("/api/gcp_resources/config", h(api.ResourcesConfig))
	mux.Handle("/api/gcp_resources/overview", h(api.ResourcesOverview))
	mux.Handle("/api/gcp_resources/compute", h(api.ResourcesCompute))
	mux.Handle("/api/gcp_resources/storage", h(api.ResourcesStorage))
	mux.Handle("/api/gcp_resources/network", h(api.ResourcesNetwork))
	mux.Handle("/api/gcp_resources/explorer", h(api.ResourcesExplorer))
	mux.Handle("/api/gcp_resources/insights", h(api.ResourcesInsights))

	// Individual endpoints
	// Pricing calculator: pure math over the list-price catalog, no BigQuery.
	mux.Handle("/api/calculator/presets", h(api.CalculatorPresets))
	mux.Handle("/api/calculator/storage", h(api.CalculateStorage))
	mux.Handle("/api/calculator/slots", h(api.CalculateSlots))

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

// Unwrap lets http.ResponseController reach the underlying writer; without
// it, per-request deadline extensions (long imports) silently fail.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

type gzipResponseWriter struct {
	http.ResponseWriter
	Writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

