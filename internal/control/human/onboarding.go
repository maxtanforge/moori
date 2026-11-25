package human

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) agents(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ListHumanAgents(r.Context(), h.getSession(r), r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) createAgentToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	issued, err := h.store.CreateHumanAgentToken(r.Context(), h.getSession(r), in.ID, r.Header.Get("Idempotency-Key"))
	if errors.Is(err, storage.ErrConflict) {
		http.Error(w, "Agent 标识已被使用。请选择新标识。已有节点的 Token 可在主服务通过 CLI 轮换。", http.StatusConflict)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issued)
}

func (h *handler) provisionTarget(w http.ResponseWriter, r *http.Request) {
	var in storage.TargetSetup
	if !decode(w, r, &in) {
		return
	}
	if in.Local && !h.localExecution {
		http.Error(w, "本机 Docker 执行功能尚未启用。", http.StatusConflict)
		return
	}
	if err := h.store.ProvisionHumanTarget(r.Context(), h.getSession(r), in); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			http.Error(w, "目标名称已被使用，或执行机器已发生变化。请检查名称并刷新 Agent 列表。", http.StatusConflict)
			return
		}
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Generation int64 `json:"generation"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := h.store.DeleteHumanAgent(r.Context(), h.getSession(r), r.PathValue("agent"), in.Generation)
	if errors.Is(err, storage.ErrTargetBusy) {
		writeErrorEnvelope(w, http.StatusConflict, "target_busy", "节点仍有待处理或执行中的任务，请处理完成后再删除。")
		return
	}
	if errors.Is(err, storage.ErrConflict) {
		writeErrorEnvelope(w, http.StatusConflict, "conflict", "节点身份已更新，请刷新后重试。")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
