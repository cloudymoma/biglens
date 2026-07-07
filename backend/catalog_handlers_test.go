package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestHandler builds an APIHandler backed by a temp OKF bundle, with no
// BigQuery client (catalog endpoints never touch it).
func newTestHandler(t *testing.T) *APIHandler {
	t.Helper()
	return &APIHandler{
		cache:  NewCache(time.Minute),
		bundle: writeBundle(t, map[string]string{
			"tables/users":  "---\ntype: BigQuery Table\ntitle: Users\ntags: [pii]\n---\nLinks [orders](/tables/orders).",
			"tables/orders": "---\ntype: BigQuery Table\ntitle: Orders\n---\nNo links.",
			"glossary/pii":  "---\ntype: Glossary Term\ntitle: PII\n---\nSensitive.",
		}),
	}
}

func TestCatalogGraphEndpoint(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/graph", nil)
	rec := httptest.NewRecorder()
	h.CatalogGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var g Graph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3", len(g.Nodes))
	}
	if len(g.Edges) != 1 { // users->orders only (in-bundle)
		t.Errorf("edges = %d, want 1", len(g.Edges))
	}
}

func TestCatalogSearchEndpoint(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/search?type=Glossary+Term", nil)
	rec := httptest.NewRecorder()
	h.CatalogSearch(rec, req)

	var nodes []GraphNode
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Type != "Glossary Term" {
		t.Errorf("got %+v, want 1 Glossary Term", nodes)
	}
}

func TestCatalogConceptUpsertAndDelete(t *testing.T) {
	h := newTestHandler(t)

	// Warm the graph cache, then ensure a write invalidates it.
	h.CatalogGraph(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/catalog/graph", nil))
	if _, ok := h.cache.Get(catalogGraphCacheKey); !ok {
		t.Fatal("expected graph to be cached")
	}

	// PUT a new concept.
	body, _ := json.Marshal(Concept{ID: "metrics/revenue", Type: "Metric", Title: "Revenue"})
	putReq := httptest.NewRequest(http.MethodPut, "/api/catalog/concept", bytes.NewReader(body))
	putRec := httptest.NewRecorder()
	h.CatalogConcept(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d (%s)", putRec.Code, putRec.Body.String())
	}
	if _, ok := h.cache.Get(catalogGraphCacheKey); ok {
		t.Error("write should have invalidated graph cache")
	}

	// Graph now has 4 nodes.
	gRec := httptest.NewRecorder()
	h.CatalogGraph(gRec, httptest.NewRequest(http.MethodGet, "/api/catalog/graph", nil))
	var g Graph
	json.Unmarshal(gRec.Body.Bytes(), &g)
	if len(g.Nodes) != 4 {
		t.Errorf("after upsert nodes = %d, want 4", len(g.Nodes))
	}

	// DELETE it.
	delReq := httptest.NewRequest(http.MethodDelete, "/api/catalog/concept?id=metrics/revenue", nil)
	delRec := httptest.NewRecorder()
	h.CatalogConcept(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", delRec.Code)
	}

	// Reading the deleted concept 404s.
	getRec := httptest.NewRecorder()
	h.CatalogConcept(getRec, httptest.NewRequest(http.MethodGet, "/api/catalog/concept?id=metrics/revenue", nil))
	if getRec.Code != http.StatusNotFound {
		t.Errorf("get deleted status = %d, want 404", getRec.Code)
	}
}

func TestCatalogUpsertRejectsTraversal(t *testing.T) {
	h := newTestHandler(t)
	body, _ := json.Marshal(Concept{ID: "../../escape", Type: "Metric"})
	rec := httptest.NewRecorder()
	h.CatalogConcept(rec, httptest.NewRequest(http.MethodPut, "/api/catalog/concept", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for traversal", rec.Code)
	}
}
