package human

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
)

func MountImageArchives(mux *http.ServeMux, store *storage.Store, relay *imagearchive.Relay, proxies ...netip.Prefix) {
	h := &handler{store: store, trustedProxies: proxies}
	handle := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("operation")
		action, err := store.HumanImageTransfer(r.Context(), h.getSession(r), r.PathValue("agent"), id, time.Now())
		if err != nil {
			writeError(w, err)
			return
		}
		switch {
		case r.Method == http.MethodGet && action == nodeops.ImageExport:
			imagearchive.ReceiveHTTP(w, r, relay, id, "docker-image.tar")
		case r.Method == http.MethodPost && action == nodeops.ImageImport:
			imagearchive.SendHTTP(w, r, relay, id)
		default:
			http.Error(w, "image transfer direction does not match the operation", http.StatusConflict)
		}
	}
	const path = "/api/v1/nodes/{agent}/image-transfers/{operation}"
	mux.HandleFunc("GET "+path, h.guard(handle))
	mux.HandleFunc("POST "+path, h.guard(handle))
}
