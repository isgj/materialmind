package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"materialmind/internal/acpinternal"
	"materialmind/internal/engine"
)

func (a *API) callACPInternalTool(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "ACP internal MCP token is required")
		return
	}
	var request acpinternal.BrokerRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	output, err := a.engine.CallACPInternalTool(
		r.Context(),
		token,
		strings.TrimSpace(request.ToolName),
		request.Arguments,
	)
	if err != nil {
		if errors.Is(err, engine.ErrACPInternalUnauthorized) {
			writeError(w, http.StatusUnauthorized, "ACP internal MCP token is invalid")
			return
		}
		if errors.Is(err, engine.ErrACPInternalUnavailable) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeAPIError(w, err)
		return
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acpinternal.BrokerResponse{Output: encoded})
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
