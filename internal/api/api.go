package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/auth"
	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/profiles"
	"github.com/onatm/boxface/internal/secrets"
	"github.com/onatm/boxface/internal/sshkeys"
	"github.com/onatm/boxface/internal/version"
)

const maxBody = 1 << 20

type Server struct {
	Auth        *auth.Store
	Lifecycle   *lifecycle.Service
	Images      *images.Registry
	SSHKeys     *sshkeys.Store
	Secrets     *secrets.Store
	Profiles    *profiles.Store
	Builds      interface{ Kick() }
	Credentials interface {
		PushOwner(ctx context.Context, ownerID string)
	}
	Gateway       Gateway
	DialGuest     func(ctx context.Context, address string) (net.Conn, error)
	Capacity      lifecycle.Capacity
	PollInterval  time.Duration
	LoginFailures int
	LoginWindow   time.Duration

	limiter *loginLimiter
}

type ownerKey struct{}

type tokenKey struct{}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *Server) Handler() http.Handler {
	if s.LoginFailures == 0 {
		s.LoginFailures = 10
	}
	if s.LoginWindow == 0 {
		s.LoginWindow = 10 * time.Minute
	}
	if s.DialGuest == nil {
		s.DialGuest = dialGuest
	}
	s.limiter = newLoginLimiter(s.LoginFailures, s.LoginWindow)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/login", s.login)
	s.webRoutes(mux)
	authed := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.authenticate(h)) }
	authed("GET /v1/capabilities", s.capabilities)
	authed("GET /v1/identities", s.listIdentities)
	authed("POST /v1/identities", s.bindIdentity)
	authed("DELETE /v1/identities/{id}", s.unbindIdentity)
	authed("POST /v1/login-codes", s.issueLoginCode)
	authed("GET /v1/tokens", s.listTokens)
	authed("DELETE /v1/tokens/{id}", s.revokeToken)
	authed("POST /v1/images", s.importImage)
	authed("GET /v1/images", s.listImages)
	authed("GET /v1/image-imports", s.listStaged)
	authed("PUT /v1/image-aliases/{name}", s.setImageAlias)
	authed("GET /v1/defaults", s.getDefaults)
	authed("PUT /v1/defaults", s.setDefaults)
	authed("GET /v1/ssh-keys", s.listSSHKeys)
	authed("POST /v1/ssh-keys", s.addSSHKey)
	authed("DELETE /v1/ssh-keys/{id}", s.deleteSSHKey)
	authed("GET /v1/profiles", s.listProfiles)
	authed("POST /v1/profiles", s.createProfile)
	authed("GET /v1/profiles/{id}", s.getProfile)
	authed("POST /v1/profiles/{id}/revisions", s.reviseProfile)
	authed("GET /v1/secrets", s.listSecrets)
	authed("POST /v1/secrets", s.createSecret)
	authed("PUT /v1/secrets/{id}", s.updateSecret)
	authed("DELETE /v1/secrets/{id}", s.deleteSecret)
	authed("POST /v1/environments", s.createEnvironment)
	authed("GET /v1/environments", s.listEnvironments)
	authed("GET /v1/environments/{id}", s.getEnvironment)
	authed("POST /v1/environments/{id}/start", s.startEnvironment)
	authed("POST /v1/environments/{id}/stop", s.stopEnvironment)
	authed("DELETE /v1/environments/{id}", s.deleteEnvironment)
	authed("GET /v1/environments/{id}/connection", s.connection)
	authed("GET /v1/environments/{id}/ssh", s.sshTunnel)
	authed("GET /v1/operations/{id}", s.getOperation)
	authed("GET /v1/operations/{id}/events", s.operationEvents)
	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var owner, tokenID string
		var err error
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			owner, tokenID, err = s.Auth.Authenticate(r.Context(), token)
		} else if c, cerr := r.Cookie(sessionCookie); cerr == nil {
			owner, tokenID, err = s.Auth.AuthenticateSession(r.Context(), c.Value)
		} else {
			err = auth.ErrUnauthorized
		}
		if err != nil {
			writeError(w, err)
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), ownerKey{}, owner), tokenKey{}, tokenID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func owner(r *http.Request) string { return r.Context().Value(ownerKey{}).(string) }

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", lifecycle.ErrInvalid, err)
	}
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	addr := clientAddr(r)
	if !s.limiter.allow(addr) {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.LoginWindow.Seconds())))
		writeError(w, errRateLimited)
		return
	}
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	var token, ownerID string
	var err error
	if req.Code == "" {
		provider, subject, ok := requestIdentity(r)
		if !ok {
			writeError(w, errNoIdentity)
			return
		}
		token, ownerID, err = s.Auth.LoginIdentity(r.Context(), provider, subject, req.Name, addr)
	} else {
		token, ownerID, err = s.Auth.Login(r.Context(), req.Code, req.Name, addr)
		if errors.Is(err, auth.ErrUnauthorized) {
			s.limiter.fail(addr)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "owner_id": ownerID})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auth.Tokens(r.Context(), owner(r), r.Context().Value(tokenKey{}).(string))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.Revoke(r.Context(), owner(r), r.PathValue("id"), clientAddr(r)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tokenID(r *http.Request) string { return r.Context().Value(tokenKey{}).(string) }

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auth.Identities(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": list})
}

func (s *Server) bindIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	provider, subject, ok := requestIdentity(r)
	if !ok || req.Provider != provider {
		writeError(w, fmt.Errorf("%w: binding a %s login needs a request through tailscale serve from a device signed in as that user", auth.ErrInvalidIdentity, req.Provider))
		return
	}
	ident, created, err := s.Auth.BindIdentity(r.Context(), owner(r), provider, subject, tokenID(r), clientAddr(r))
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, ident)
}

func (s *Server) unbindIdentity(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.UnbindIdentity(r.Context(), owner(r), r.PathValue("id"), tokenID(r), clientAddr(r)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      version.Version,
		"architecture": runtime.GOARCH,
		"hypervisor":   "firecracker",
		"capacity": map[string]int{
			"environments": s.Capacity.Environments,
			"vcpus":        s.Capacity.VCPUs,
			"memory_mib":   s.Capacity.MemoryMiB,
			"disk_gib":     s.Capacity.DiskGiB,
		},
	})
}

func (s *Server) importImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	img, created, err := s.Images.Import(r.Context(), req.Source)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, img)
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	list, err := s.Images.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": list})
}

func (s *Server) setImageAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID string `json:"image_id"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Images.SetAlias(r.Context(), r.PathValue("name"), req.ImageID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "image_id": req.ImageID})
}

func (s *Server) getDefaults(w http.ResponseWriter, r *http.Request) {
	d, err := s.Lifecycle.Defaults(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	saved, err := s.Lifecycle.DefaultsSaved(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		lifecycle.Defaults
		Saved bool `json:"saved"`
	}{d, saved})
}

func (s *Server) setDefaults(w http.ResponseWriter, r *http.Request) {
	var d lifecycle.Defaults
	if err := decode(w, r, &d); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Lifecycle.SetDefaults(r.Context(), owner(r), d); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.SSHKeys.List(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ssh_keys": list})
}

func (s *Server) addSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, created, err := s.SSHKeys.Add(r.Context(), owner(r), req.Name, req.PublicKey)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, k)
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	if err := s.SSHKeys.Delete(r.Context(), owner(r), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.Profiles.List(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": list})
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string        `json:"name"`
		Spec profiles.Spec `json:"spec"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	p, err := s.Profiles.Create(r.Context(), owner(r), req.Name, req.Spec)
	if err != nil {
		writeError(w, err)
		return
	}
	s.auditProfile(r, "profile.create", p.Name)
	s.kickBuilds()
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Profiles.Get(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) reviseProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Spec profiles.Spec `json:"spec"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	rev, err := s.Profiles.AddRevision(r.Context(), owner(r), r.PathValue("id"), req.Spec)
	if err != nil {
		writeError(w, err)
		return
	}
	s.auditProfile(r, "profile.revise", r.PathValue("id"))
	s.kickBuilds()
	writeJSON(w, http.StatusCreated, rev)
}

func (s *Server) kickBuilds() {
	if s.Builds != nil {
		s.Builds.Kick()
	}
}

func (s *Server) auditProfile(r *http.Request, action, target string) {
	if err := s.Auth.Record(r.Context(), owner(r), action, tokenID(r), "profile:"+target, clientAddr(r)); err != nil {
		slog.Error("record profile audit event", "action", action, "error", err)
	}
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	list, err := s.Secrets.List(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": list, "suggested": secrets.Suggested, "session": secrets.Session})
}

func (s *Server) createSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string            `json:"name"`
		Value    string            `json:"value"`
		Bindings []secrets.Binding `json:"bindings"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	sec, err := s.Secrets.Create(r.Context(), owner(r), req.Name, req.Value, req.Bindings)
	if err != nil {
		writeError(w, err)
		return
	}
	s.auditSecret(r, "secret.create", sec.Name)
	writeJSON(w, http.StatusCreated, sec)
}

func (s *Server) updateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value    *string            `json:"value"`
		Bindings *[]secrets.Binding `json:"bindings"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	sec, err := s.Secrets.Update(r.Context(), owner(r), r.PathValue("id"), req.Value, req.Bindings)
	if err != nil {
		writeError(w, err)
		return
	}
	s.auditSecret(r, "secret.update", sec.Name)
	writeJSON(w, http.StatusOK, sec)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	name, err := s.Secrets.Delete(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	s.auditSecret(r, "secret.delete", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) auditSecret(r *http.Request, action, name string) {
	if s.Credentials != nil {
		s.Credentials.PushOwner(context.WithoutCancel(r.Context()), owner(r))
	}
	if err := s.Auth.Record(r.Context(), owner(r), action, tokenID(r), "secret:"+name, clientAddr(r)); err != nil {
		slog.Error("record secret audit event", "action", action, "error", err)
	}
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req lifecycle.CreateRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	acc, err := s.Lifecycle.Create(r.Context(), owner(r), r.Header.Get("Idempotency-Key"), req)
	writeAccepted(w, acc, err)
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.Lifecycle.Environments(r.Context(), owner(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, err := s.Lifecycle.Environment(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) startEnvironment(w http.ResponseWriter, r *http.Request) {
	acc, err := s.Lifecycle.Start(r.Context(), owner(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"))
	writeAccepted(w, acc, err)
}

func (s *Server) stopEnvironment(w http.ResponseWriter, r *http.Request) {
	acc, err := s.Lifecycle.Stop(r.Context(), owner(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"))
	writeAccepted(w, acc, err)
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	purge := false
	if v := r.URL.Query().Get("purge"); v != "" {
		var err error
		if purge, err = strconv.ParseBool(v); err != nil {
			writeError(w, fmt.Errorf("%w: purge must be true or false", lifecycle.ErrInvalid))
			return
		}
	}
	acc, err := s.Lifecycle.Delete(r.Context(), owner(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"), purge)
	writeAccepted(w, acc, err)
}

func (s *Server) getOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Lifecycle.Operation(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func (s *Server) operationEvents(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	op, err := s.Lifecycle.Operation(ctx, owner(r), id)
	if err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming unsupported"))
		return
	}
	last, _ := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	for {
		for i, step := range op.CompletedSteps {
			if i+1 > last {
				writeEvent(w, i+1, "step", map[string]string{"step": step})
				last = i + 1
			}
		}
		if terminal(op.State) {
			if len(op.CompletedSteps)+1 > last {
				writeEvent(w, len(op.CompletedSteps)+1, "state", op)
			}
			flusher.Flush()
			return
		}
		flusher.Flush()
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.PollInterval):
		}
		if op, err = s.Lifecycle.Operation(ctx, owner(r), id); err != nil {
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, id int, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, b)
}

func writeAccepted(w http.ResponseWriter, acc lifecycle.Accepted, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/v1/operations/"+acc.OperationID)
	writeJSON(w, http.StatusAccepted, acc)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	for _, m := range []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrUnauthorized, http.StatusUnauthorized, "unauthorized"},
		{errRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{auth.ErrInvalidName, http.StatusBadRequest, "invalid_request"},
		{auth.ErrTokenNotFound, http.StatusNotFound, "not_found"},
		{auth.ErrInvalidIdentity, http.StatusBadRequest, "invalid_request"},
		{auth.ErrIdentityNotFound, http.StatusNotFound, "not_found"},
		{auth.ErrIdentityTaken, http.StatusConflict, "identity_taken"},
		{lifecycle.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{images.ErrInvalidSource, http.StatusBadRequest, "invalid_request"},
		{images.ErrInvalidAlias, http.StatusBadRequest, "invalid_request"},
		{sshkeys.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{lifecycle.ErrNotFound, http.StatusNotFound, "not_found"},
		{images.ErrNotFound, http.StatusNotFound, "not_found"},
		{sshkeys.ErrNotFound, http.StatusNotFound, "not_found"},
		{profiles.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{profiles.ErrNotFound, http.StatusNotFound, "not_found"},
		{profiles.ErrNameTaken, http.StatusConflict, "name_taken"},
		{profiles.ErrNotReady, http.StatusConflict, "profile_not_ready"},
		{secrets.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{secrets.ErrNotFound, http.StatusNotFound, "not_found"},
		{secrets.ErrConflict, http.StatusConflict, "conflict"},
		{lifecycle.ErrNameTaken, http.StatusConflict, "name_taken"},
		{lifecycle.ErrEnvironmentDeleted, http.StatusConflict, "environment_deleted"},
		{lifecycle.ErrImageNotReady, http.StatusConflict, "image_not_ready"},
		{lifecycle.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		{lifecycle.ErrCapacity, http.StatusConflict, "capacity_exceeded"},
		{errNotRunning, http.StatusConflict, "environment_not_running"},
		{errGuestUnreachable, http.StatusBadGateway, "guest_unreachable"},
	} {
		if errors.Is(err, m.err) {
			status, code = m.status, m.code
			break
		}
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		slog.Error("request failed", "error", err)
		message = "internal error"
	}
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message, Retryable: status >= 500 || status == http.StatusTooManyRequests}})
}
