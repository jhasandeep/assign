package api

import (
	"errors"
	"net/http"

	"sutra/internal/store"
)

func (a *API) getIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	issue, err := a.store.GetIssue(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, http.StatusNotFound, "issue not found", nil)
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, "could not load issue", err)
	default:
		writeJSON(w, http.StatusOK, issue)
	}
}

func (a *API) listVouchers(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !store.ValidStatus(status) {
		a.fail(w, r, http.StatusBadRequest, "status must be pending, pushed or failed", nil)
		return
	}
	vs, err := a.store.ListVouchers(r.Context(), status)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not list vouchers", err)
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

func (a *API) retryVoucher(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	v, err := a.store.RetryVoucher(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, http.StatusNotFound, "voucher not found", nil)
	case errors.Is(err, store.ErrAlreadyPushed):
		a.fail(w, r, http.StatusConflict, "voucher is already pushed; retrying could double-post", nil)
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, "could not retry voucher", err)
	default:
		writeJSON(w, http.StatusOK, v)
	}
}
