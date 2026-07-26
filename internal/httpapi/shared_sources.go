package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (a *API) listSharedSources(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListSharedSources(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) getSharedSource(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.GetSharedSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}
