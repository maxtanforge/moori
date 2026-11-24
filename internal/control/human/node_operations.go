package human

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
)

type RunLocalNodeOperation func(context.Context, nodeops.Request) (nodeops.Result, error)

func (h *handler) submitNodeOperation(w http.ResponseWriter, r *http.Request) {
	var req nodeops.Request
	if !decodeTemplate(w, r, &req) {
		return
	}
	if req.Generation <= 0 || req.Validate() != nil {
		writeError(w, storage.ErrInvalid)
		return
	}
	agent := r.PathValue("agent")
	token := h.getSession(r)
	ctx := r.Context()
	admin, err := h.store.HumanPlatformAdmin(ctx, token)
	if err != nil {
		writeError(w, err)
		return
	}
	if !admin {
		writeError(w, storage.ErrAccessDenied)
		return
	}
	if agent == storage.LocalExecutorID && !h.localExecution {
		http.Error(w, "local Docker execution is unavailable", http.StatusServiceUnavailable)
		return
	}
	if scope, image, ok := req.RegistryAccess(); ok && (req.Action.Registry() || req.Edit != nil && req.Edit.PullScope != nil) {
		if err := h.store.AuthorizeRegistryOperation(ctx, token, agent, scope.Project, scope.Target, scope.RegistryID, image); err != nil {
			writeNodeOperationError(w, err)
			return
		}
	}
	var op nodeops.Operation
	direct := agent == storage.LocalExecutorID && h.runLocalOperation != nil && req.Action.LocalDirect()
	if direct {
		op, err = h.store.StartLocalNodeOperation(ctx, token, req.Generation, req, time.Now())
	} else {
		op, err = h.store.QueueNodeOperation(ctx, token, agent, req.Generation, req, time.Now())
	}
	if err != nil {
		writeNodeOperationError(w, err)
		return
	}
	status := http.StatusAccepted
	if direct && op.State == nodeops.Running {
		op = h.finishLocalNodeOperation(ctx, token, op, req)
		if op.State != nodeops.Running {
			status = http.StatusOK
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(op)
}

func (h *handler) finishLocalNodeOperation(ctx context.Context, token string, op nodeops.Operation, req nodeops.Request) nodeops.Operation {
	req.Operation = op.ID
	runCtx, cancel := context.WithTimeout(ctx, req.Action.Timeout())
	defer cancel()
	result, err := h.runLocalOperation(runCtx, req)
	state, failure, output := localNodeOperationOutcome(runCtx, req.Action, result, err)

	// Record the outcome even if the browser disconnects during Docker execution.
	saveCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if h.store.CompleteNodeOperation(saveCtx, op.Agent, op.Generation, op.ID, state, failure, output, time.Now()) != nil {
		return op
	}
	finished, err := h.store.GetNodeOperation(saveCtx, token, op.Agent, op.ID, time.Now())
	if err != nil {
		return op
	}
	return finished
}

func localNodeOperationOutcome(ctx context.Context, action nodeops.Action, result nodeops.Result, err error) (nodeops.State, string, *nodeops.Result) {
	if err != nil {
		if action.Mutates() && (ctx.Err() != nil || errors.Is(err, nodeops.ErrOutcomeUnknown) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nodeops.Uncertain, "Docker did not confirm the result. Refresh resources before retrying.", nil
		}
		message := err.Error()
		message = message[:min(len(message), 500)]
		for !utf8.ValidString(message) && message != "" {
			message = message[:len(message)-1]
		}
		if message == "" {
			message = "the Docker operation failed"
		}
		return nodeops.Failed, message, nil
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil || len(encoded) > nodeops.MaxResultBytes || len(result.Log) > 3*nodeops.MaxLogBytes {
		return nodeops.Failed, "the Docker response exceeded the operation output limit", nil
	}
	return nodeops.Succeeded, "", &result
}

func (h *handler) listNodeOperationHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if limitParam := query.Get("limit"); limitParam != "" || query.Get("after") != "" {
		limit, err := strconv.Atoi(limitParam)
		if err != nil || query.Get("id") != "" {
			writeNodeOperationError(w, storage.ErrInvalid)
			return
		}
		page, err := h.store.PageNodeOperationHistory(r.Context(), h.getSession(r), r.PathValue("agent"), query.Get("after"), limit, time.Now())
		if err != nil {
			writeNodeOperationError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
		return
	}
	entries, err := h.store.ListNodeOperationHistory(r.Context(), h.getSession(r), r.PathValue("agent"), r.URL.Query().Get("id"), time.Now())
	if err != nil {
		writeNodeOperationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func (h *handler) getNodeOperation(w http.ResponseWriter, r *http.Request) {
	op, err := h.store.GetNodeOperation(r.Context(), h.getSession(r), r.PathValue("agent"), r.PathValue("operation"), time.Now())
	if err != nil {
		writeNodeOperationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(op)
}

// writeNodeOperationError reports node-operation failures with the same
// machine-readable envelope as the rest of the console API, so the frontend
// can branch on code uniformly.
func writeNodeOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNodeOperationsUnavailable):
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "unavailable", "this node has not recently advertised Docker management")
	case errors.Is(err, storage.ErrNodeOperationsBusy):
		writeErrorEnvelope(w, http.StatusConflict, "target_busy", "this node has too many queued Docker operations")
	case errors.Is(err, registry.ErrAmbiguous):
		writeErrorEnvelope(w, http.StatusConflict, "registry_ambiguous", registry.ErrAmbiguous.Error())
	case errors.Is(err, registry.ErrDenied):
		writeErrorEnvelope(w, http.StatusConflict, "conflict", "the registry is unavailable for this node and scope")
	default:
		writeError(w, err)
	}
}
