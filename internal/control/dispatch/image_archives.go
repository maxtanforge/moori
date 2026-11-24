package dispatch

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/transport/httpproxy"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
)

func ImageArchiveEndpoint(store *storage.Store, relay *imagearchive.Relay, proxies ...netip.Prefix) http.Handler {
	authenticate := AuthenticateCredential(store)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !httpproxy.HTTPS(r, proxies) {
			http.Error(w, "remote image archives require verified HTTPS", http.StatusUpgradeRequired)
			return
		}
		peer, err := authenticate(r)
		if err != nil {
			http.Error(w, "Agent identity rejected", http.StatusUnauthorized)
			return
		}
		id := r.PathValue("operation")
		request, err := store.RunningNodeOperation(r.Context(), peer.ID, peer.Generation, id, time.Now())
		if err != nil {
			http.Error(w, "image transfer is not available", http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && request.Action == nodeops.ImageImport:
			imagearchive.ReceiveHTTP(w, r, relay, id, "")
		case r.Method == http.MethodPost && request.Action == nodeops.ImageExport:
			imagearchive.SendHTTP(w, r, relay, id)
		default:
			http.Error(w, "image transfer direction does not match the operation", http.StatusForbidden)
		}
	})
}
