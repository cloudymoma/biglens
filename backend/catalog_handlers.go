package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"time"
)

const catalogGraphCacheKey = "catalog_graph"

// --- Dataplex / Knowledge Catalog (OKF bundle) endpoints ---

// CatalogGraph: GET /api/catalog/graph — full node+edge graph of the bundle.
func (h *APIHandler) CatalogGraph(w http.ResponseWriter, r *http.Request) {
	if cached, ok := h.cache.Get(catalogGraphCacheKey); ok {
		writeJSON(w, cached)
		return
	}
	g, err := h.bundle.BuildGraph()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.cache.Set(catalogGraphCacheKey, g)
	writeJSON(w, g)
}

// CatalogSearch: GET /api/catalog/search?q=&type= — search concepts.
func (h *APIHandler) CatalogSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	typeFilter := r.URL.Query().Get("type")
	nodes, err := h.bundle.Search(q, typeFilter)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, nodes)
}

// CatalogConcept handles /api/catalog/concept:
//   GET    ?id=   -> one concept + neighbors
//   PUT           -> upsert a concept (JSON body, writes OKF markdown)
//   DELETE ?id=   -> delete a concept
func (h *APIHandler) CatalogConcept(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		h.upsertConcept(w, r)
	case http.MethodDelete:
		h.deleteConcept(w, r)
	default:
		h.getConcept(w, r)
	}
}

func (h *APIHandler) getConcept(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, "id is required", http.StatusBadRequest)
		return
	}
	detail, err := h.bundle.GetConcept(id)
	if err != nil {
		writeError(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, detail)
}

func (h *APIHandler) upsertConcept(w http.ResponseWriter, r *http.Request) {
	var c Concept
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, "invalid concept body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.ID == "" {
		writeError(w, "concept id is required", http.StatusBadRequest)
		return
	}
	c.UserManaged = true
	if err := h.bundle.WriteConcept(c); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.cache.Delete(catalogGraphCacheKey)
	detail, err := h.bundle.GetConcept(c.ID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, detail)
}

func (h *APIHandler) deleteConcept(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, "id is required", http.StatusBadRequest)
		return
	}
	if err := h.bundle.DeleteConcept(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.cache.Delete(catalogGraphCacheKey)
	writeJSON(w, map[string]string{"deleted": id})
}

// CatalogImport: POST /api/catalog/import?q= — import Dataplex entries into
// the OKF bundle. Builds the Dataplex client on demand.
func (h *APIHandler) CatalogImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Imports legitimately outlive the server-wide 60s WriteTimeout (one
	// aspect + one lineage RPC per entry); extend this request's deadline so
	// the response isn't cut off mid-import ("Network Error" in the UI).
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(10 * time.Minute)
	_ = rc.SetWriteDeadline(deadline)
	_ = rc.SetReadDeadline(deadline)

	// refresh=1 re-runs the import recorded in manifest.yaml.
	query, err := importQueryFor(r, h.bundle)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	cc, err := NewCatalogClient(r.Context(), h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer cc.Close()

	result, err := cc.Import(r.Context(), h.bundle, query)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.cache.Delete(catalogGraphCacheKey)
	writeJSON(w, result)
}

// CatalogManifest: GET /api/catalog/manifest — the recorded scope of the last
// import (404 when nothing has been imported yet).
func (h *APIHandler) CatalogManifest(w http.ResponseWriter, r *http.Request) {
	m, err := h.bundle.ReadManifest()
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, "no import recorded yet", http.StatusNotFound)
			return
		}
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, m)
}

// CatalogTypes: GET /api/catalog/types — distinct concept types with counts,
// used to build the legend and type filters in the UI.
func (h *APIHandler) CatalogTypes(w http.ResponseWriter, r *http.Request) {
	concepts, err := h.bundle.ListConcepts()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	counts := map[string]int{}
	for _, c := range concepts {
		t := c.Type
		if t == "" {
			t = "Untyped"
		}
		counts[t]++
	}
	type typeCount struct {
		Type  string `json:"type"`
		Count int    `json:"count"`
	}
	out := make([]typeCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, typeCount{Type: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	writeJSON(w, out)
}
