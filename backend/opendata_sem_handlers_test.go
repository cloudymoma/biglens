package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/option"
)

// The SEM endpoints must reject malformed requests before any BigQuery call
// is issued — every accepted request costs a partition scan, so validation is
// the cost guard. bq is nil: reaching a query would panic, proving the
// validation short-circuits.
func TestSemMetaValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "", "market"},
		{"unknown market", "market=emea", "market"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/meta?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemMeta(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

func TestSemDashboardValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "refresh_date=2026-08-16&geo=US", "market"},
		{"unknown market", "market=apac&refresh_date=2026-08-16&geo=US", "market"},
		{"missing refresh_date", "market=us", "refresh_date"},
		{"malformed refresh_date", "market=us&refresh_date=08/16/2026", "refresh_date"},
		{"global without geo", "market=global&refresh_date=2026-08-16", "geo"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/dashboard?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemDashboard(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

// The geo and term endpoints share parseSemTermSelection; both must reject a
// request missing any of market / refresh_date / geo-for-global / term.
func TestSemTermSelectionValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "refresh_date=2026-08-16&geo=US&term=x", "market"},
		{"unknown market", "market=apac&refresh_date=2026-08-16&geo=US&term=x", "market"},
		{"missing refresh_date", "market=us&term=x", "refresh_date"},
		{"malformed refresh_date", "market=us&refresh_date=08/16/2026&term=x", "refresh_date"},
		{"global without geo", "market=global&refresh_date=2026-08-16&term=x", "geo"},
		{"missing term", "market=us&refresh_date=2026-08-16", "term"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}
	handlers := map[string]http.HandlerFunc{
		"geo":  h.SemGeo,
		"term": h.SemTerm,
	}

	for endpoint, handler := range handlers {
		for _, tt := range tests {
			t.Run(endpoint+"/"+tt.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/"+endpoint+"?"+tt.query, nil)
				w := httptest.NewRecorder()

				handler(w, r)

				if w.Code != http.StatusBadRequest {
					t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
				}
				if !strings.Contains(w.Body.String(), tt.wantMsg) {
					t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
				}
			})
		}
	}
}

func TestSemSafetyValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "", "market"},
		{"unknown market", "market=emea", "market"},
		{"global without geo", "market=global", "geo"},
		// Trends countries without a GDELT actor-code mapping must be
		// rejected up front, not passed through as a silent empty result.
		{"unmapped country", "market=global&geo=ZZ", "mapping"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/safety?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemSafety(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

// --- W2 geo interest: query shape, NULL handling, error hygiene ---

// fakeBigQuery stands in for the BigQuery REST API behind a real
// *bigquery.Client: it answers the client's jobs.query fast path with one
// canned result set (or one canned error) and records the SQL and parameters
// of every query, so handler tests can pin the query text, the NULL decoding
// and the JSON a handler writes without credentials or a billed project.
type fakeBigQuery struct {
	columns []fakeBQColumn
	rows    [][]any // REST-encoded cells (INTEGER as string); nil is SQL NULL
	status  int     // non-zero: fail every query with this HTTP status
	errMsg  string

	mu       sync.Mutex
	requests []fakeBQRequest
}

type fakeBQColumn struct{ name, typ string }

type fakeBQRequest struct {
	sql    string
	params map[string]string
}

func (f *fakeBigQuery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/queries") {
		http.Error(w, "fake BigQuery only serves jobs.query, got "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		return
	}
	var req struct {
		Query           string `json:"query"`
		QueryParameters []struct {
			Name           string `json:"name"`
			ParameterValue struct {
				Value string `json:"value"`
			} `json:"parameterValue"`
		} `json:"queryParameters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params := make(map[string]string, len(req.QueryParameters))
	for _, p := range req.QueryParameters {
		params[p.Name] = p.ParameterValue.Value
	}
	f.mu.Lock()
	f.requests = append(f.requests, fakeBQRequest{sql: req.Query, params: params})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":    f.status,
			"message": f.errMsg,
			"errors":  []map[string]string{{"reason": "accessDenied", "message": f.errMsg}},
		}})
		return
	}
	fields := make([]map[string]string, len(f.columns))
	for i, c := range f.columns {
		fields[i] = map[string]string{"name": c.name, "type": c.typ, "mode": "NULLABLE"}
	}
	rows := make([]map[string]any, len(f.rows))
	for i, row := range f.rows {
		cells := make([]map[string]any, len(row))
		for j, v := range row {
			cells[j] = map[string]any{"v": v}
		}
		rows[i] = map[string]any{"f": cells}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"kind":         "bigquery#queryResponse",
		"jobReference": map[string]string{"projectId": "test-project", "jobId": "fake-job", "location": "US"},
		"jobComplete":  true,
		"schema":       map[string]any{"fields": fields},
		"rows":         rows,
		"totalRows":    strconv.Itoa(len(rows)),
	})
}

// sent returns a copy of the queries received so far.
func (f *fakeBigQuery) sent() []fakeBQRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeBQRequest(nil), f.requests...)
}

// newFakeBQHandler returns an APIHandler whose BigQuery client talks to f.
func newFakeBQHandler(t *testing.T, f *fakeBigQuery) *APIHandler {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return &APIHandler{bq: &BQClient{client: client}, cache: NewCache(time.Minute)}
}

// semGeoColumns is the result schema of the W2 geo queries.
var semGeoColumns = []fakeBQColumn{
	{"geo", "STRING"}, {"week", "STRING"}, {"score", "INTEGER"},
	{"rising_rank", "INTEGER"}, {"percent_gain", "INTEGER"},
}

func getSemGeo(t *testing.T, h *APIHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.SemGeo(w, httptest.NewRequest(http.MethodGet, "/api/opendata/sem/geo?"+query, nil))
	return w
}

// W2 must read the latest *complete* week of the pinned partition. The US
// tables also carry the in-progress week that starts on refresh_date itself
// (partition 2026-10-04 ends with week 2026-10-04), where most DMAs are still
// NULL; a Sunday-start week W is complete once W + 7 days <= refresh_date.
// Missing scores must stay NULL instead of being coalesced to 0 (a 0 reads as
// "no interest" and used to earn a -90% bid suggestion) and must sort last;
// the partition filter must stay a plain parameter comparison so BigQuery
// prunes to the one partition.
func TestSemGeoQueriesLatestCompleteWeek(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		tables     []string
		wantParams map[string]string
	}{
		{
			name:       "us",
			query:      "market=us&refresh_date=2026-10-04&term=usury",
			tables:     []string{semUSTopTable, semUSRisingTable},
			wantParams: map[string]string{"refresh_date": "2026-10-04", "term": "usury"},
		},
		{
			name:       "global",
			query:      "market=global&refresh_date=2026-10-04&geo=GB&term=weather",
			tables:     []string{trendsTopTable, trendsRisingTable},
			wantParams: map[string]string{"refresh_date": "2026-10-04", "country_code": "GB", "term": "weather"},
		},
	}
	// A score coalesced to 0: COALESCE(AVG(score), 0), COALESCE(c.score, 0), ...
	scoreToZero := regexp.MustCompile(`COALESCE\([^,]*score[^,]*, 0\)`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeBigQuery{columns: semGeoColumns, rows: [][]any{{"Somewhere", "2026-09-27", "100", "0", "0"}}}
			w := getSemGeo(t, newFakeBQHandler(t, fake), tt.query)
			if w.Code != http.StatusOK {
				t.Fatalf("got status %d (%s), want 200", w.Code, w.Body.String())
			}
			reqs := fake.sent()
			if len(reqs) != 1 {
				t.Fatalf("got %d queries, want 1", len(reqs))
			}
			sql := strings.Join(strings.Fields(reqs[0].sql), " ")

			for _, table := range tt.tables {
				pin := "week = (SELECT MAX(week) FROM " + table +
					" WHERE refresh_date = @refresh_date AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date)"
				if !strings.Contains(sql, pin) {
					t.Errorf("%s: week must be pinned to the latest complete week %q:\n%s", table, pin, sql)
				}
			}
			if n := strings.Count(sql, "SELECT MAX(week)"); n != len(tt.tables) {
				t.Errorf("got %d MAX(week) pins, want one per table (%d):\n%s", n, len(tt.tables), sql)
			}
			if n := strings.Count(sql, "WHERE refresh_date = @refresh_date"); n < 2*len(tt.tables) {
				t.Errorf("every scan must filter the partition by @refresh_date, got %d filters:\n%s", n, sql)
			}
			if m := scoreToZero.FindAllString(sql, -1); len(m) > 0 {
				t.Errorf("missing scores must stay NULL, found %q:\n%s", m, sql)
			}
			if !strings.Contains(sql, "ORDER BY score DESC NULLS LAST") {
				t.Errorf("geos without a score must sort last:\n%s", sql)
			}
			for name, want := range tt.wantParams {
				if got := reqs[0].params[name]; got != want {
					t.Errorf("param @%s = %q, want %q", name, got, want)
				}
			}
			for _, v := range []string{tt.wantParams["term"], tt.wantParams["refresh_date"]} {
				if strings.Contains(sql, v) {
					t.Errorf("%q must be bound as a query parameter, not inlined:\n%s", v, sql)
				}
			}
		})
	}
}

// A geo below the Trends reporting threshold has no score for the week: the
// handler must pass it through as JSON null (shown as "insufficient data"),
// not 0, and report which week the rows describe.
func TestSemGeoSerializesMissingScoreAsNull(t *testing.T) {
	fake := &fakeBigQuery{columns: semGeoColumns, rows: [][]any{
		{"New York NY", "2026-09-27", "100", "0", "0"},
		{"Glendive MT", "2026-09-27", nil, "0", "0"},
	}}
	w := getSemGeo(t, newFakeBQHandler(t, fake), "market=us&refresh_date=2026-10-04&term=usury")
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d (%s), want 200", w.Code, w.Body.String())
	}

	var got struct {
		Week string `json:"week"`
		Rows []struct {
			Geo   string          `json:"geo"`
			Score json.RawMessage `json:"score"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if got.Week != "2026-09-27" {
		t.Errorf("week = %q, want 2026-09-27", got.Week)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(got.Rows), w.Body.String())
	}
	if s := string(got.Rows[0].Score); s != "100" {
		t.Errorf("%s: score = %s, want 100", got.Rows[0].Geo, s)
	}
	if s := string(got.Rows[1].Score); s != "null" {
		t.Errorf("%s: score = %s, want null (insufficient data)", got.Rows[1].Geo, s)
	}
}

// Query failures are logged server-side; the client gets a generic message,
// never the raw BigQuery error (project, dataset and table names, SQL).
func TestSemGeoHidesQueryErrors(t *testing.T) {
	fake := &fakeBigQuery{
		status: http.StatusForbidden,
		errMsg: "Access Denied: Table secret-project:internal_ds.t: User does not have permission",
	}
	w := getSemGeo(t, newFakeBQHandler(t, fake), "market=us&refresh_date=2026-10-04&term=usury")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got status %d, want 500", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "internal_ds") || strings.Contains(body, "googleapi") {
		t.Errorf("response leaks the raw BigQuery error: %q", body)
	}
}
