package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/option"
)

// jobsInsertFake stands in for BigQuery's jobs.insert behind a real
// *bigquery.Client: it records the configuration of every inserted job,
// answers dry runs with a finished job and fails real jobs (the test only
// needs the request a raw q.Run sends).
type jobsInsertFake struct {
	mu   sync.Mutex
	jobs []insertedJob
}

type insertedJob struct {
	labels   map[string]string
	dryRun   bool
	maxBytes string
}

func (f *jobsInsertFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/jobs") {
		http.Error(w, "fake only serves jobs.insert, got "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		return
	}
	var job struct {
		JobReference  map[string]any `json:"jobReference"`
		Configuration struct {
			Labels map[string]string `json:"labels"`
			DryRun bool              `json:"dryRun"`
			Query  struct {
				MaximumBytesBilled string `json:"maximumBytesBilled"`
			} `json:"query"`
		} `json:"configuration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.jobs = append(f.jobs, insertedJob{
		labels:   job.Configuration.Labels,
		dryRun:   job.Configuration.DryRun,
		maxBytes: job.Configuration.Query.MaximumBytesBilled,
	})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !job.Configuration.DryRun {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": 400, "message": "fake: real jobs are not run",
			"errors": []map[string]string{{"reason": "invalid", "message": "fake: real jobs are not run"}},
		}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"jobReference": job.JobReference,
		"status":       map[string]any{"state": "DONE"},
		"statistics": map[string]any{
			"totalBytesProcessed": "4242",
			"query":               map[string]any{"totalBytesProcessed": "4242"},
		},
	})
}

func (f *jobsInsertFake) sent() []insertedJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]insertedJob(nil), f.jobs...)
}

// BigLens jobs carry app=biglens so the tool's own spend can be told apart in
// JOBS_BY_PROJECT; the freeze sync's raw q.Run jobs used to skip it. The
// label must not change the byte cap: Fetch keeps the caller's cap and the
// free dry run stays uncapped.
func TestStablecoinJobsCarryAppLabel(t *testing.T) {
	fake := &jobsInsertFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	src := bqStablecoinSource{client: client}
	start := civil.Date{Year: 2026, Month: 10, Day: 1}
	end := civil.Date{Year: 2026, Month: 10, Day: 2}

	est, err := src.DryRun(context.Background(), start, end)
	if err != nil || est != 4242 {
		t.Fatalf("DryRun = %d, %v; want 4242, nil", est, err)
	}
	const fetchCap = int64(150 << 30)
	if _, _, err := src.Fetch(context.Background(), start, end, fetchCap); err == nil {
		t.Fatal("Fetch: want the fake's error for a real job")
	}

	jobs := fake.sent()
	if len(jobs) != 2 {
		t.Fatalf("got %d inserted jobs, want 2 (dry run + fetch)", len(jobs))
	}
	dry, fetch := jobs[0], jobs[1]
	if dry.labels["app"] != "biglens" || !dry.dryRun || dry.maxBytes != "" {
		t.Errorf("dry run job = %+v, want app=biglens, dryRun, no byte cap", dry)
	}
	if fetch.labels["app"] != "biglens" || fetch.dryRun || fetch.maxBytes != strconv.FormatInt(fetchCap, 10) {
		t.Errorf("fetch job = %+v, want app=biglens and the caller's cap %d", fetch, fetchCap)
	}
}
