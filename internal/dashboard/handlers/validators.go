package handlers

import (
	"net/http"

	"github.com/CTJaeger/KleverNodeHub/internal/dashboard/klever"
)

// ValidatorsHandler serves the validator-monitoring snapshot built by the
// background Klever monitor.
type ValidatorsHandler struct {
	monitor *klever.Monitor
}

// NewValidatorsHandler creates a new ValidatorsHandler.
func NewValidatorsHandler(monitor *klever.Monitor) *ValidatorsHandler {
	return &ValidatorsHandler{monitor: monitor}
}

// HandleSnapshot handles GET /api/validators — returns the latest monitor
// snapshot (cards summary + per-validator stats + block-production timeline).
func (h *ValidatorsHandler) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.monitor.Snapshot())
}

// HandleElections handles GET /api/validators/elections — returns the monthly
// election history (epochs each managed validator was elected, per calendar
// month), for the "elected this month" column and the long-term chart.
func (h *ValidatorsHandler) HandleElections(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.monitor.ElectionHistory())
}
