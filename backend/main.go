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
	backfill := flag.Bool("address-risk-backfill", false, "backfill USDT/USDC/EURC freeze history from BigQuery, then exit")
	since := flag.String("since", "", "backfill from this date, YYYY-MM-DD (default 2017-11-28: full history)")
	sinceDays := flag.Int("since-days", 0, "backfill the last N days (dev only; excludes --since)")
	yes := flag.Bool("yes", false, "run the backfill; without it only a dry-run estimate is printed")
	force := flag.Bool("force", false, "bypass the minimum interval lock for --bitcoin-utxo-baseline (never bypasses budget)")
	btcBaseline := flag.Bool("bitcoin-utxo-baseline", false, "build the Bitcoin UTXO baseline from BigQuery (dry run unless --yes), then exit")
	btcResume := flag.String("bitcoin-bq-resume", "", "clear the stop flag for a Bitcoin BigQuery job (btc_onchain or btc_coinage) and exit")
	stableVerify := flag.Bool("stablecoin-verify", false, "check every enabled stablecoin source against its live endpoint, print a pass/fail table and exit (non-zero on any failure)")
	flag.Parse()

	cfg, err := LoadConfig("conf.yaml")
	if err != nil {
		cfg, err = LoadConfig("../conf.yaml")
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}
	}

	if *btcResume != "" {
		os.Exit(runBtcResumeCLI(context.Background(), cfg, *btcResume))
	}
	if *stableVerify {
		// Public HTTPS and RPC reads only: no BigQuery client is created.
		os.Exit(runStableVerifyCLI(context.Background(), cfg))
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
	if *btcBaseline {
		os.Exit(runBtcBaselineCLI(ctx, cfg, bq, *yes, *force))
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
	invalidate := func() { invalidateAddressRiskCaches(api.cache) }
	if s, err := openRiskStore(cfg.AddressRisk.dbPath()); err != nil {
		slog.Error("address risk store unavailable", "path", cfg.AddressRisk.dbPath(), "error", err)
	} else {
		rstore = s
		lists := newRiskListSyncer(rstore)
		lists.onChange = invalidate
		stable := &stablecoinSyncer{store: rstore, src: bqStablecoinSource{client: bq.client}, now: time.Now,
			initialDays: cfg.AddressRisk.initialSyncDays(), onChange: invalidate}
		syncJobs := []func(context.Context){lists.syncDue, stable.syncOnce}
		for _, job := range newScamRadarJobs(bq.client) {
			job.allowZeroCandidates = cfg.AddressRisk.ScamRadar.AllowZeroCandidates
			syncer := &dailyBQSyncer{
				store:         rstore,
				job:           job,
				now:           time.Now,
				initialDays:   cfg.AddressRisk.scamRadarInitialDays(),
				retentionDays: cfg.AddressRisk.scamRadarRetentionDays(),
				onChange:      invalidate,
			}
			syncJobs = append(syncJobs, syncer.syncOnce)
		}
		go runAddressRiskSync(ctx, time.Hour, syncJobs...)
	}
	// On-chain USD prices (Uniswap v3 swaps in BigQuery → SQLite). Its own
	// syncer, run at startup and hourly by the same loop helper; it keeps
	// working from memory when the SQLite store is unavailable.
	prices := newCryptoPriceSyncer(bqCryptoPriceSource{client: bq.client}, rstore, cfg.CryptoPrices.initialDays(), time.Now)
	api.prices = prices
	go runAddressRiskSync(ctx, time.Hour, prices.syncOnce)
	if api.btc != nil {
		api.btc.store = rstore
		api.btc.read = newBtcReadStore(rstore, api.btc.settings, time.Now)
		api.btc.prices = newBtcPriceSyncer(rstore, api.btc.settings, time.Now)
		api.btc.binance = newBinanceAdapter(rstore, time.Now)
		api.btc.nodes = newBtcNodesSyncer(rstore, api.btc.settings, "data/geo", time.Now)
		mktSnap := newBtcMarketSnapshotter(api, rstore, time.Now)
		if api.btc.settings.Jobs && api.btc.settings.BaselineAllowUI && rstore != nil {
			api.btc.starter = newBtcBaselineRunner(rstore, api.btc.settings, bqBaselineSource{bq: bq}, time.Now)
		}
		go runAddressRiskSync(ctx, time.Hour, api.btc.prices.syncOnce, mktSnap.syncOnce, api.btc.nodes.hourlyTick)
		if bqJobs := bitcoinSyncJobs(api.btc.settings, rstore, bq, api.btc.prices); len(bqJobs) > 0 {
			go runAddressRiskSync(ctx, time.Hour, bqJobs...)
		}
	}
	gasCfg := cfg.CryptoGas.withDefaults()
	api.risk = newAddressRiskService(rstore, cfg.AddressRisk.rpcURLs())
	api.risk.chainRPCs = map[string][]string{
		"eth":  cfg.AddressRisk.rpcURLs(),
		"arb":  gasCfg.ArbitrumRPCURLs,
		"op":   gasCfg.OptimismRPCURLs,
		"base": gasCfg.BaseRPCURLs,
		"sol":  gasCfg.SolanaRPCURLs,
	}
	api.risk.blockscoutURLs = cfg.PaymentCheck.BlockscoutURLs
	if cfg.PaymentCheck.LogsMaxSpan > 0 {
		payLogsMaxSpan = cfg.PaymentCheck.LogsMaxSpan
	}
	api.risk.cfg = cfg
	api.risk.bqSrc = bqStablecoinSource{client: bq.client}
	api.risk.bqProfile = newBQProfiler(cfg.AddressRisk.bigQueryProfile(), bqProfileClient{client: bq.client})
	api.risk.invalidateCache = invalidate
	api.risk.setEtherscanKey(cfg.AddressRisk.EtherscanAPIKey)
	// Stablecoins dashboard: keyless public APIs and RPC reads only (no
	// BigQuery in v1). Started after api.risk so the sampler's RPC lists
	// (payRPCs) are final.
	if api.stable != nil {
		api.stable.store = rstore
		api.stable.health = newStableHealth(api.stable.settings, time.Now)
		api.stable.supply = newStableSupplySampler(api.stable.settings, rstore, defaultStableEndpoints(), api.payRPCs, time.Now)
		api.stable.supply.onFetch = api.stable.health.recordFetch
		api.stable.peg = newStablePegSampler(api.stable.settings, rstore, defaultStableEndpoints(), api.payRPCs, time.Now)
		api.stable.peg.onFetch = api.stable.health.recordFetch
		api.stable.controls = newStableControlsSampler(api.stable.settings, rstore, api.payRPCs, time.Now)
		api.stable.controls.onFetch = api.stable.health.recordFetch
		api.stable.controls.gas = func(time.Time) { api.stable.refreshGasCosts(api.stableGasCosts()) }
		api.stable.wireSignals()
		go runStableSamplers(ctx, api.stable.supply, api.stable.health)
		go runStablePegSampler(ctx, api.stable.peg, api.stable.health)
		go runStableControlsSampler(ctx, api.stable.controls, api.stable.health)
		go runStableSignalWatchdog(ctx, api.stable)
	}

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
	registerCryptoRoutes(mux, api, prices, h, logMW)
	registerBitcoinRoutes(mux, api, h, logMW)
	registerStablecoinRoutes(mux, api, h)
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
	mux.Handle("/", logMW(staticFileHandler(staticDir)))

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

// staticFileHandler serves the built SPA with Cache-Control: no-cache on
// index.html / unhashed assets so browsers always revalidate the entry HTML
// after a rebuild, and immutable caching on content-hashed /assets/ bundles.
func staticFileHandler(staticDir string) http.Handler {
	fs := http.FileServer(http.Dir(staticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(&staticCacheWriter{ResponseWriter: w}, r)
	})
}

type staticCacheWriter struct {
	http.ResponseWriter
}

func (w *staticCacheWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *staticCacheWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.ResponseWriter.WriteHeader(status)
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
