package human

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/registry"
)

func MountRegistries(mux *http.ServeMux, store *storage.Store, key storage.SecretKey, trustedProxies ...netip.Prefix) {
	h := &handler{store: store, trustedProxies: trustedProxies}
	mux.HandleFunc("GET /api/v1/registries", h.guard(func(w http.ResponseWriter, r *http.Request) {
		page, err := store.ListHumanRegistries(r.Context(), h.getSession(r), r.URL.Query().Get("after"))
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRegistryJSON(w, http.StatusOK, struct {
			storage.CatalogPage[storage.Registry]
			CredentialsAvailable bool `json:"credentials_available"`
		}{page, key != (storage.SecretKey{})})
	}))
	mux.HandleFunc("POST /api/v1/registries", h.guard(func(w http.ResponseWriter, r *http.Request) {
		var input storage.RegistryInput
		if !decode(w, r, &input) {
			return
		}
		item, err := store.CreateHumanRegistry(r.Context(), h.getSession(r), key, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRegistryJSON(w, http.StatusCreated, item)
	}))
	mux.HandleFunc("POST /api/v1/registries/{registry}/credential", h.guard(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		err := store.RotateHumanRegistryCredential(r.Context(), h.getSession(r), key, r.PathValue("registry"), input.Username, input.Password)
		finishRegistryMutation(w, err)
	}))
	mux.HandleFunc("POST /api/v1/registries/{registry}/details", h.guard(func(w http.ResponseWriter, r *http.Request) {
		var input storage.RegistryInput
		if !decode(w, r, &input) {
			return
		}
		err := store.UpdateHumanRegistry(r.Context(), h.getSession(r), r.PathValue("registry"), input)
		finishRegistryMutation(w, err)
	}))
	mux.HandleFunc("POST /api/v1/registries/{registry}/enabled", h.guard(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Enabled == nil {
			writeRegistryError(w, storage.ErrInvalid)
			return
		}
		err := store.SetHumanRegistryEnabled(r.Context(), h.getSession(r), r.PathValue("registry"), *input.Enabled)
		finishRegistryMutation(w, err)
	}))
	mux.HandleFunc("POST /api/v1/registries/{registry}/bindings", h.guard(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			storage.RegistryBinding
			Enabled *bool `json:"enabled"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Enabled == nil {
			writeRegistryError(w, storage.ErrInvalid)
			return
		}
		err := store.SetHumanRegistryBinding(r.Context(), h.getSession(r), r.PathValue("registry"), input.RegistryBinding, *input.Enabled)
		finishRegistryMutation(w, err)
	}))
}

func writeRegistryJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func finishRegistryMutation(w http.ResponseWriter, err error) {
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrSecretKey):
		http.Error(w, "镜像仓库凭证无法保存：请恢复与数据库配套的原加密密钥并重启主服务。密钥存放在自定义位置时可配置 MOORI_SECRET_KEY_FILE。", http.StatusServiceUnavailable)
	case errors.Is(err, registry.ErrDenied):
		http.Error(w, "registry scope is unavailable", http.StatusConflict)
	case errors.Is(err, storage.ErrRegistryHostBound):
		http.Error(w, "已保存的访问凭证与原地址加密绑定，不能修改地址；如需更换地址请新建仓库并重新授权。", http.StatusConflict)
	case errors.Is(err, storage.ErrConflict):
		http.Error(w, "与已有仓库配置冲突：名称重复，或同一节点的仓库地址范围重叠。", http.StatusConflict)
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "仓库信息格式无效：请检查名称、地址与路径前缀（例如 registry.example.com/namespace）。", http.StatusBadRequest)
	default:
		writeError(w, err)
	}
}
