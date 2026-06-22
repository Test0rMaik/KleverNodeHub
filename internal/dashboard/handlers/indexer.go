package handlers

import (
	"net/http"

	"github.com/CTJaeger/KleverNodeHub/internal/dashboard/indexer"
)

// IndexerHandler serves the indexer node health snapshot.
type IndexerHandler struct {
	monitor *indexer.Monitor
}

// NewIndexerHandler creates a new IndexerHandler.
func NewIndexerHandler(monitor *indexer.Monitor) *IndexerHandler {
	return &IndexerHandler{monitor: monitor}
}

// HandleSnapshot handles GET /api/indexer/status.
func (h *IndexerHandler) HandleSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.monitor.Snapshot())
}
