package auth

import (
	"net/http"

	"github.com/q15co/q15/systems/web/internal/gate"
)

func (a *Authenticator) unauthorized(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodGet {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	gate.ServeShell(w, a.shell, http.StatusUnauthorized)
}
