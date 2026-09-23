package main

// HTTP handlers for the pricing calculator (/api/calculator/*). Stateless:
// every request carries its inputs and gets a full estimate back.

import (
	"encoding/json"
	"net/http"
)

func (h *APIHandler) CalculatorPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, calculatorPresets())
}

func (h *APIHandler) CalculateStorage(w http.ResponseWriter, r *http.Request) {
	var req StorageCalcRequest
	if !decodeCalcRequest(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, storageEstimate(req))
}

func (h *APIHandler) CalculateSlots(w http.ResponseWriter, r *http.Request) {
	var req SlotsCalcRequest
	if !decodeCalcRequest(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, slotsEstimate(req))
}

func decodeCalcRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(dst); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}
