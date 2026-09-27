package quotation

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

const TenantHeader = "X-Tenant-Id"

const requestLimit = 1 << 20

type API struct {
	service *Service
	tenants map[string]bool
}

func NewAPI(service *Service, tenants []string) *API {
	known := make(map[string]bool, len(tenants))
	for _, t := range tenants {
		known[t] = true
	}
	return &API{service: service, tenants: known}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /quotes", a.quote)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		platform.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func (a *API) quote(w http.ResponseWriter, r *http.Request) {
	tenant := r.Header.Get(TenantHeader)
	if tenant == "" {
		platform.WriteError(w, http.StatusBadRequest, TenantHeader+" is required")
		return
	}

	if !a.tenants[tenant] {
		platform.WriteError(w, http.StatusForbidden, "broker not enabled on this platform")
		return
	}

	var request Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, requestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		platform.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := request.Normalize(); err != nil {
		platform.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response, err := a.service.Quote(r.Context(), tenant, request)
	if err != nil {
		a.respondPartnerFailure(w, err)
		return
	}

	if len(response.Quotes) == 0 {
		platform.WriteJSON(w, http.StatusServiceUnavailable, noPartnerQuoteAvailable{
			Error:           "no partner quote available",
			TenantID:        tenant,
			MissingPartners: response.MissingPartners,
		})
		return
	}

	w.Header().Set("X-Tenant-Id", tenant)
	platform.WriteJSON(w, http.StatusOK, response)
}

// noPartnerQuoteAvailable is the 503 body when every partner failed for this
// request (FDD seção 5); distinct from platform.ErrorBody because it always
// carries tenant_id and the full list of missing partners.
type noPartnerQuoteAvailable struct {
	Error           string   `json:"error"`
	TenantID        string   `json:"tenant_id"`
	MissingPartners []string `json:"missing_partners"`
}

func (a *API) respondPartnerFailure(w http.ResponseWriter, err error) {
	var failure *partner.Error
	if errors.As(err, &failure) {
		platform.WriteJSON(w, http.StatusBadGateway, platform.ErrorBody{
			Message: "partner insurer unavailable",
			Partner: failure.Partner,
		})
		return
	}
	platform.WriteError(w, http.StatusBadGateway, "failed to query the partner insurers")
}
