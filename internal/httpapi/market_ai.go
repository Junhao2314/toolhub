package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/market"
)

// searchMarket queries the configured marketplace sources and returns normalized
// listings. A single unreachable source never blocks the remaining sources; those
// failures surface under the response `errors` map instead.
func (a *API) searchMarket(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := a.market.Search(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("q"), page, limit)
	if err != nil {
		selector := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
		switch {
		case errors.Is(err, market.ErrRateLimited):
			message := "Marketplace rate limit is exhausted; retry after the provider window resets"
			if selector == "skillsmp" && a.config.SkillsMPAPIKey == "" {
				message = "SkillsMP anonymous quota is exhausted; configure SKILLSMP_API_KEY and retry"
			}
			writeError(w, r, http.StatusTooManyRequests, "market_rate_limited", message)
		case errors.Is(err, market.ErrUnknownSource):
			writeError(w, r, http.StatusBadRequest, "invalid_source", "source must be all, skillsmp, or xiaping")
		case errors.Is(err, market.ErrInvalidQuery):
			writeError(w, r, http.StatusBadRequest, "invalid_request", "search query must contain 2-200 characters")
		default:
			writeError(w, r, http.StatusBadGateway, "market_unavailable", "The selected marketplace sources are unavailable")
		}
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(w, http.StatusOK, result)
}

func (a *API) recommend(w http.ResponseWriter, r *http.Request) {
	var input ai.Request
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	candidates, err := a.recommender.Recommend(r.Context(), input)
	if err != nil {
		code := "recommendation_failed"
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "not found") {
			code, status = "ai_provider_missing", http.StatusPreconditionFailed
		}
		writeError(w, r, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates, "automaticInstall": false})
}
