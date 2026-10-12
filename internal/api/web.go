package api

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/onatm/boxface/internal/auth"
)

const (
	sessionCookie = "boxface_session"
	webCodeTTL    = 15 * time.Minute
)

//go:embed web
var webFiles embed.FS

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) webRoutes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFiles, "web")
	index := func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, static, "index.html") }
	mux.Handle("GET /{$}", secureHeaders(http.HandlerFunc(index)))
	mux.Handle("GET /login", secureHeaders(http.HandlerFunc(index)))
	mux.Handle("GET /assets/", secureHeaders(http.StripPrefix("/assets/", http.FileServerFS(static))))
	mux.HandleFunc("GET /web/session", s.webSession)
	mux.HandleFunc("POST /web/login", s.webLogin)
	mux.HandleFunc("POST /web/logout", s.webLogout)
}

func secureRequest(r *http.Request) bool {
	via, _ := r.Context().Value(tailscaleKey{}).(bool)
	return r.TLS != nil || via
}

func (s *Server) webSession(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"signed_in": false}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if ownerID, _, err := s.Auth.AuthenticateSession(r.Context(), c.Value); err == nil {
			name, _ := s.Auth.OwnerName(r.Context(), ownerID)
			out["signed_in"], out["owner"] = true, name
		}
	}
	if provider, subject, ok := requestIdentity(r); ok {
		bound, err := s.Auth.IdentityBound(r.Context(), provider, subject)
		if err != nil {
			writeError(w, err)
			return
		}
		out["identity"] = map[string]any{"provider": provider, "subject": subject, "bound": bound}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	addr := clientAddr(r)
	if !s.limiter.allow(addr) {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.LoginWindow.Seconds())))
		writeError(w, errRateLimited)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	var secret string
	var err error
	if req.Code == "" {
		provider, subject, ok := requestIdentity(r)
		if !ok {
			writeError(w, errNoIdentity)
			return
		}
		secret, _, err = s.Auth.WebLoginIdentity(r.Context(), provider, subject, addr)
	} else {
		secret, _, err = s.Auth.WebLogin(r.Context(), req.Code, addr)
		if errors.Is(err, auth.ErrUnauthorized) {
			s.limiter.fail(addr)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", MaxAge: int(auth.SessionTTL.Seconds()),
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Auth.EndSession(r.Context(), c.Value); err != nil {
			writeError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) issueLoginCode(w http.ResponseWriter, r *http.Request) {
	code, err := s.Auth.IssueCode(r.Context(), owner(r), webCodeTTL)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"code": code, "expires_at": time.Now().Add(webCodeTTL).UTC().Format(time.RFC3339)})
}

func (s *Server) listStaged(w http.ResponseWriter, r *http.Request) {
	list, err := s.Images.Staged()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"staged": list})
}
