package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/market"
)

func (a *API) searchMarket(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := a.market.Search(r.Context(), r.URL.Query().Get("q"), page, limit)
	if errors.Is(err, market.ErrRateLimited) {
		message := "SkillsMP anonymous quota is exhausted; configure SKILLSMP_API_KEY and retry"
		if a.config.SkillsMPAPIKey != "" {
			message = "SkillsMP rate limit is exhausted; retry after the provider window resets"
		}
		writeError(w, r, http.StatusTooManyRequests, "market_rate_limited", message)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "market_unavailable", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
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
