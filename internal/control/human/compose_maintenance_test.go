package human

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExternalComposePasteRoutesUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	(&handler{}).mountComposeMaintenance(mux)
	for _, action := range []string{"preview", "apply"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/@local/compose-projects/external-app/"+action, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want %d", action, w.Code, http.StatusNotFound)
		}
	}
}
