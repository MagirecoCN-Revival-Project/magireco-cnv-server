// Package captcha 暴露 cap-worker 兼容的 HTTP 端点(/api/challenge /api/redeem)。
package captcha

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/capworker"
)

type Handler struct {
	Svc *capworker.Service
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/challenge", h.challenge)
	r.Post("/redeem", h.redeem)
}

func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	c, err := h.Svc.Issue(r.Context())
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	respond.JSON(w, http.StatusOK, c)
}

type redeemReq struct {
	Token     string  `json:"token"`
	Solutions []int64 `json:"solutions"`
}

func (h *Handler) redeem(w http.ResponseWriter, r *http.Request) {
	var req redeemReq
	if !respond.ReadJSON(w, r, &req) {
		return
	}
	result := h.Svc.Redeem(r.Context(), req.Token, req.Solutions)
	respond.JSON(w, http.StatusOK, result)
}
