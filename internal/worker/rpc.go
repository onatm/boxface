package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/onatm/boxface/internal/lifecycle"
)

type Backend interface {
	lifecycle.Worker
	Inventory(ctx context.Context) (Inventory, error)
	Endpoint(ctx context.Context, environmentID string) (netip.Addr, error)
	HostKey(ctx context.Context, diskID string) (string, error)
	SetCredentials(ctx context.Context, environmentID string, creds lifecycle.Credentials) error
	StartBuild(ctx context.Context, spec lifecycle.BuildSpec) error
	Storage(ctx context.Context) (Storage, error)
	ProfileDrives(ctx context.Context) ([]string, error)
	RemoveProfileDrive(ctx context.Context, key string) error
	RemoveBuildCache(ctx context.Context, ownerID string) error
	BuildStatus(ctx context.Context, buildID string) (lifecycle.BuildStatus, error)
}

type credentialsRequest struct {
	Credentials lifecycle.Credentials `json:"credentials"`
}

type endpointResponse struct {
	Address netip.Addr `json:"address"`
}

type hostKeyResponse struct {
	HostKey string `json:"host_key"`
}

type idRequest struct {
	ID string `json:"id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(b Backend) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, call func(r *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := call(r); err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	byID := func(call func(ctx context.Context, id string) error) func(r *http.Request) error {
		return func(r *http.Request) error {
			var req idRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return fmt.Errorf("decode request: %w", err)
			}
			return call(r.Context(), req.ID)
		}
	}
	handle("POST /v1/disks/create", func(r *http.Request) error {
		var spec lifecycle.DiskSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		return b.CreateDisk(r.Context(), spec)
	})
	handle("POST /v1/disks/remove", byID(b.RemoveDisk))
	handle("POST /v1/networks/create", byID(b.CreateNetwork))
	handle("POST /v1/networks/remove", byID(b.RemoveNetwork))
	handle("POST /v1/vms/start", func(r *http.Request) error {
		var spec lifecycle.VMSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		return b.StartVM(r.Context(), spec)
	})
	handle("POST /v1/vms/stop", byID(b.StopVM))
	handle("POST /v1/builds/start", func(r *http.Request) error {
		var spec lifecycle.BuildSpec
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&spec); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		return b.StartBuild(r.Context(), spec)
	})
	handle("PUT /v1/environments/{id}/credentials", func(r *http.Request) error {
		var req credentialsRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			return errors.New("decode credentials request")
		}
		return b.SetCredentials(r.Context(), r.PathValue("id"), req.Credentials)
	})
	handle("POST /v1/vms/wait-ready", byID(b.WaitReady))
	get := func(pattern string, call func(r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v, err := call(r)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, v)
		})
	}
	get("GET /v1/inventory", func(r *http.Request) (any, error) { return b.Inventory(r.Context()) })
	get("GET /v1/builds/{id}", func(r *http.Request) (any, error) { return b.BuildStatus(r.Context(), r.PathValue("id")) })
	get("GET /v1/storage", func(r *http.Request) (any, error) { return b.Storage(r.Context()) })
	get("GET /v1/profile-drives", func(r *http.Request) (any, error) { return b.ProfileDrives(r.Context()) })
	handle("DELETE /v1/profile-drives/{key}", func(r *http.Request) error { return b.RemoveProfileDrive(r.Context(), r.PathValue("key")) })
	handle("DELETE /v1/build-caches/{owner}", func(r *http.Request) error { return b.RemoveBuildCache(r.Context(), r.PathValue("owner")) })
	get("GET /v1/environments/{id}/endpoint", func(r *http.Request) (any, error) {
		addr, err := b.Endpoint(r.Context(), r.PathValue("id"))
		return endpointResponse{addr}, err
	})
	get("GET /v1/disks/{id}/host-key", func(r *http.Request) (any, error) {
		key, err := b.HostKey(r.Context(), r.PathValue("id"))
		return hostKeyResponse{key}, err
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type PeerListener struct {
	net.Listener
	Allow func(uid uint32) bool
}

func (p PeerListener) Accept() (net.Conn, error) {
	for {
		conn, err := p.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(conn)
		if err == nil && p.Allow(uid) {
			return conn, nil
		}
		slog.Warn("rejected worker connection", "uid", uid, "error", err)
		conn.Close()
	}
}

type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://worker"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: %v", lifecycle.ErrWorkerUnavailable, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode == http.StatusOK && out != nil:
		return json.NewDecoder(resp.Body).Decode(out)
	default:
		var e errorResponse
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Error == "" {
			return fmt.Errorf("worker %s: unexpected status %s", path, resp.Status)
		}
		return errors.New(e.Error)
	}
}

func (c *Client) CreateDisk(ctx context.Context, spec lifecycle.DiskSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/disks/create", spec, nil)
}

func (c *Client) RemoveDisk(ctx context.Context, diskID string) error {
	return c.do(ctx, http.MethodPost, "/v1/disks/remove", idRequest{diskID}, nil)
}

func (c *Client) CreateNetwork(ctx context.Context, environmentID string) error {
	return c.do(ctx, http.MethodPost, "/v1/networks/create", idRequest{environmentID}, nil)
}

func (c *Client) RemoveNetwork(ctx context.Context, environmentID string) error {
	return c.do(ctx, http.MethodPost, "/v1/networks/remove", idRequest{environmentID}, nil)
}

func (c *Client) StartVM(ctx context.Context, spec lifecycle.VMSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/vms/start", spec, nil)
}

func (c *Client) StopVM(ctx context.Context, environmentID string) error {
	return c.do(ctx, http.MethodPost, "/v1/vms/stop", idRequest{environmentID}, nil)
}

func (c *Client) WaitReady(ctx context.Context, environmentID string) error {
	return c.do(ctx, http.MethodPost, "/v1/vms/wait-ready", idRequest{environmentID}, nil)
}

func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	var inv Inventory
	err := c.do(ctx, http.MethodGet, "/v1/inventory", nil, &inv)
	return inv, err
}

func (c *Client) Endpoint(ctx context.Context, environmentID string) (netip.Addr, error) {
	var resp endpointResponse
	err := c.do(ctx, http.MethodGet, "/v1/environments/"+url.PathEscape(environmentID)+"/endpoint", nil, &resp)
	return resp.Address, err
}

func (c *Client) HostKey(ctx context.Context, diskID string) (string, error) {
	var resp hostKeyResponse
	err := c.do(ctx, http.MethodGet, "/v1/disks/"+url.PathEscape(diskID)+"/host-key", nil, &resp)
	return resp.HostKey, err
}

func (c *Client) SetCredentials(ctx context.Context, environmentID string, creds lifecycle.Credentials) error {
	return c.do(ctx, http.MethodPut, "/v1/environments/"+url.PathEscape(environmentID)+"/credentials", credentialsRequest{creds}, nil)
}

func (c *Client) StartBuild(ctx context.Context, spec lifecycle.BuildSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/builds/start", spec, nil)
}

func (c *Client) BuildStatus(ctx context.Context, buildID string) (lifecycle.BuildStatus, error) {
	var st lifecycle.BuildStatus
	err := c.do(ctx, http.MethodGet, "/v1/builds/"+url.PathEscape(buildID), nil, &st)
	return st, err
}

func (c *Client) Storage(ctx context.Context) (Storage, error) {
	var st Storage
	err := c.do(ctx, http.MethodGet, "/v1/storage", nil, &st)
	return st, err
}

func (c *Client) ProfileDrives(ctx context.Context) ([]string, error) {
	var keys []string
	err := c.do(ctx, http.MethodGet, "/v1/profile-drives", nil, &keys)
	return keys, err
}

func (c *Client) RemoveProfileDrive(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/v1/profile-drives/"+url.PathEscape(key), nil, nil)
}

func (c *Client) RemoveBuildCache(ctx context.Context, ownerID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/build-caches/"+url.PathEscape(ownerID), nil, nil)
}

func (c *Client) ActiveVMs(ctx context.Context) (map[string]bool, error) {
	inv, err := c.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, e := range inv.Environments {
		if e.VM {
			active[e.EnvironmentID] = true
		}
	}
	return active, nil
}
