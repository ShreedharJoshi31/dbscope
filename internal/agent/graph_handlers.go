package agent

import (
	"errors"
	"net/http"

	"github.com/dbscope/dbscope/internal/neo4j"
)

// expandNode returns a node and up to limit of its neighbours, for the graph view.
func (s *server) expandNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"nodeId"`
		Limit  int    `json:"limit"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.NodeID == "" {
		writeError(w, http.StatusBadRequest, "validation", "nodeId is required")
		return
	}
	if body.Limit == 0 {
		body.Limit = 50
	}
	if body.Limit < 1 || body.Limit > neo4j.MaxExpand {
		writeError(w, http.StatusBadRequest, "validation", "limit must be 1-200")
		return
	}
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Graph, "Graph exploration") {
		return
	}
	src, ok := s.source.(engineSource)
	if !ok {
		s.upstreamError(w, r, errUnsupported)
		return
	}
	x, err := src.Expand(r.Context(), p, password, body.NodeID, body.Limit)
	if errors.Is(err, neo4j.ErrNoNode) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}
