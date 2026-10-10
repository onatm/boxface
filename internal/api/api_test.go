package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/auth"
	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/sshkeys"
	"github.com/onatm/boxface/internal/store"
)

var testKeys = map[string]string{
	"one": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPv/3an15p2WS8pllhCNT8+Ui5iCTCbODBLNGN4kKUba one",
	"two": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPt5LCJRq9y4c8J1YZeCdDw7TP7B5LlgxGVVvhVUhQuN two",
}

type noopWorker struct{}

func (noopWorker) CreateDisk(context.Context, lifecycle.DiskSpec) error { return nil }
func (noopWorker) RemoveDisk(context.Context, string) error             { return nil }
func (noopWorker) CreateNetwork(context.Context, string) error          { return nil }
func (noopWorker) RemoveNetwork(context.Context, string) error          { return nil }
func (noopWorker) StartVM(context.Context, lifecycle.VMSpec) error      { return nil }
func (noopWorker) StopVM(context.Context, string) error                 { return nil }
func (noopWorker) WaitReady(context.Context, string) error              { return nil }

type env struct {
	t       *testing.T
	srv     *httptest.Server
	runner  *lifecycle.Runner
	tokens  map[string]string
	imageID string
	stage   func(name string)
}

func setup(t *testing.T, capacity lifecycle.Capacity) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := lifecycle.NewRunner(ctx, db, noopWorker{})
	importDir, imagesDir := filepath.Join(dir, "import"), filepath.Join(dir, "images")
	os.MkdirAll(importDir, 0o755)
	os.MkdirAll(imagesDir, 0o755)
	authStore := auth.New(db)
	s := &Server{
		Auth:         authStore,
		Lifecycle:    lifecycle.NewService(db, runner, capacity),
		Images:       images.NewRegistry(db, importDir, imagesDir),
		SSHKeys:      sshkeys.NewStore(db),
		Capacity:     capacity,
		PollInterval: 10 * time.Millisecond,
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); cancel(); runner.Wait(); db.Close() })

	e := &env{t: t, srv: srv, runner: runner, tokens: map[string]string{}}
	e.stage = func(name string) {
		os.MkdirAll(filepath.Join(importDir, name), 0o755)
		os.WriteFile(filepath.Join(importDir, name, "vmlinux"), []byte("kernel "+name), 0o644)
		os.WriteFile(filepath.Join(importDir, name, "rootfs.ext4"), []byte("rootfs "+name), 0o644)
	}
	for _, name := range []string{"one", "two"} {
		code, err := authStore.Bootstrap(context.Background(), name, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var login map[string]string
		e.do("", http.MethodPost, "/v1/login", map[string]string{"code": code}, http.StatusOK, &login)
		e.tokens[name] = login["token"]
		e.do(name, http.MethodPost, "/v1/ssh-keys", map[string]string{"name": "laptop", "public_key": testKeys[name]}, http.StatusCreated, nil)
	}
	e.stage("base")
	var img images.Image
	e.do("one", http.MethodPost, "/v1/images", map[string]string{"source": "base"}, http.StatusCreated, &img)
	e.imageID = img.ID
	return e
}

func (e *env) request(who, method, path string, body any, header map[string]string) *http.Response {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, reader)
	if token := e.tokens[who]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *env) do(who, method, path string, body any, wantStatus int, out any, header ...map[string]string) {
	e.t.Helper()
	var h map[string]string
	if len(header) > 0 {
		h = header[0]
	}
	resp := e.request(who, method, path, body, h)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
}

func (e *env) errorCode(who, method, path string, body any, wantStatus int) string {
	e.t.Helper()
	var resp struct {
		Error apiError `json:"error"`
	}
	e.do(who, method, path, body, wantStatus, &resp)
	return resp.Error.Code
}

func (e *env) createRequest(name string) lifecycle.CreateRequest {
	return lifecycle.CreateRequest{Name: name, ImageID: e.imageID, VCPUs: 1, MemoryMiB: 1024, DiskGiB: 4}
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	e.do("", http.MethodGet, "/health", nil, http.StatusOK, nil)
	if code := e.errorCode("", http.MethodGet, "/v1/environments", nil, http.StatusUnauthorized); code != "unauthorized" {
		t.Fatalf("code = %s", code)
	}
	e.tokens["forged"] = "bfx_forged"
	e.errorCode("forged", http.MethodGet, "/v1/environments", nil, http.StatusUnauthorized)
	e.errorCode("", http.MethodPost, "/v1/login", map[string]string{"code": "WRONG"}, http.StatusUnauthorized)
}

func TestCreateReturnsAcceptedAndIsIdempotent(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	key := map[string]string{"Idempotency-Key": "create-travel"}
	resp := e.request("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), key)
	var acc lifecycle.Accepted
	json.NewDecoder(resp.Body).Decode(&acc)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || acc.OperationID == "" || acc.EnvironmentID == "" || resp.Header.Get("Location") != "/v1/operations/"+acc.OperationID {
		t.Fatalf("status %d, accepted %+v, location %q", resp.StatusCode, acc, resp.Header.Get("Location"))
	}
	e.runner.Wait()

	var retry lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusAccepted, &retry, key)
	if retry != acc {
		t.Fatalf("retry = %+v, first %+v", retry, acc)
	}
	changed := e.createRequest("travel")
	changed.VCPUs = 2
	var conflict struct {
		Error apiError `json:"error"`
	}
	e.do("one", http.MethodPost, "/v1/environments", changed, http.StatusConflict, &conflict, key)
	if conflict.Error.Code != "idempotency_conflict" {
		t.Fatalf("conflict = %+v", conflict)
	}

	var op lifecycle.Operation
	e.do("one", http.MethodGet, "/v1/operations/"+acc.OperationID, nil, http.StatusOK, &op)
	var got lifecycle.Environment
	e.do("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusOK, &got)
	if op.State != "succeeded" || got.ObservedState != "running" || got.Name != "travel" {
		t.Fatalf("operation %+v, environment %+v", op, got)
	}
}

func TestOtherOwnerEnvironmentInvisible(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	var acc lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusAccepted, &acc)
	e.runner.Wait()

	e.errorCode("two", http.MethodGet, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusNotFound)
	e.errorCode("two", http.MethodGet, "/v1/operations/"+acc.OperationID, nil, http.StatusNotFound)
	e.errorCode("two", http.MethodPost, "/v1/environments/"+acc.EnvironmentID+"/stop", nil, http.StatusNotFound)
	e.errorCode("two", http.MethodDelete, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusNotFound)
	var list struct {
		Environments []lifecycle.Environment `json:"environments"`
	}
	e.do("two", http.MethodGet, "/v1/environments", nil, http.StatusOK, &list)
	if len(list.Environments) != 0 {
		t.Fatalf("owner two sees %+v", list.Environments)
	}
}

func TestOverCapacityCreateRejected(t *testing.T) {
	e := setup(t, lifecycle.Capacity{Environments: 1, MemoryMiB: 4096})
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("first"), http.StatusAccepted, nil)
	var resp struct {
		Error apiError `json:"error"`
	}
	e.do("two", http.MethodPost, "/v1/environments", e.createRequest("second"), http.StatusConflict, &resp)
	if resp.Error.Code != "capacity_exceeded" || !strings.Contains(resp.Error.Message, "environments would be 2, limit 1") || resp.Error.Retryable {
		t.Fatalf("error = %+v", resp.Error)
	}
	e.runner.Wait()
	var list struct {
		Environments []lifecycle.Environment `json:"environments"`
	}
	e.do("two", http.MethodGet, "/v1/environments", nil, http.StatusOK, &list)
	if len(list.Environments) != 0 {
		t.Fatalf("rejected create left %+v", list.Environments)
	}
}

func readEvents(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer resp.Body.Close()
	var events []string
	var id string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			events = append(events, id+" "+strings.TrimPrefix(line, "data: "))
		}
	}
	return events
}

func TestOperationEventsStreamAndResume(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	var acc lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusAccepted, &acc)

	events := readEvents(t, e.request("one", http.MethodGet, "/v1/operations/"+acc.OperationID+"/events", nil, nil))
	if len(events) != 5 || !strings.HasPrefix(events[0], `1 {"step":"create_disk"}`) || !strings.Contains(events[4], `"state":"succeeded"`) {
		t.Fatalf("events = %q", events)
	}
	resumed := readEvents(t, e.request("one", http.MethodGet, "/v1/operations/"+acc.OperationID+"/events", nil, map[string]string{"Last-Event-ID": "3"}))
	if len(resumed) != 2 || !strings.HasPrefix(resumed[0], `4 {"step":"wait_ready"}`) {
		t.Fatalf("resumed = %q", resumed)
	}
}

func TestDeleteWithPurge(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	var acc lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusAccepted, &acc)
	e.runner.Wait()
	e.errorCode("one", http.MethodDelete, "/v1/environments/"+acc.EnvironmentID+"?purge=maybe", nil, http.StatusBadRequest)
	e.do("one", http.MethodDelete, "/v1/environments/"+acc.EnvironmentID+"?purge=true", nil, http.StatusAccepted, nil)
	e.runner.Wait()
	var got lifecycle.Environment
	e.do("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusOK, &got)
	if got.ObservedState != "deleted" || got.DiskRetention != "purged" {
		t.Fatalf("environment = %+v", got)
	}
	if code := e.errorCode("one", http.MethodPost, "/v1/environments/"+acc.EnvironmentID+"/start", nil, http.StatusConflict); code != "environment_deleted" {
		t.Fatalf("code = %s", code)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	body := map[string]any{"name": "travel", "image_id": e.imageID, "vcpus": 1, "memory_mib": 1024, "disk_gib": 4, "privileged": true}
	if code := e.errorCode("one", http.MethodPost, "/v1/environments", body, http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("code = %s", code)
	}
}

func TestImageImportAndList(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	e.stage("copy")
	var img images.Image
	e.do("one", http.MethodPost, "/v1/images", map[string]string{"source": "copy"}, http.StatusCreated, &img)
	if img.ID == e.imageID {
		t.Fatal("different content reused an image")
	}
	e.errorCode("one", http.MethodPost, "/v1/images", map[string]string{"source": "../etc"}, http.StatusBadRequest)
	var list struct {
		Images []images.Image `json:"images"`
	}
	e.do("one", http.MethodGet, "/v1/images", nil, http.StatusOK, &list)
	if len(list.Images) != 2 {
		t.Fatalf("images = %+v", list.Images)
	}
}

func TestConfigurationRoutes(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	if code := e.errorCode("one", http.MethodPost, "/v1/environments", map[string]string{"name": "travel"}, http.StatusNotFound); code != "not_found" {
		t.Fatalf("create before main exists: code = %s", code)
	}
	e.errorCode("one", http.MethodPut, "/v1/image-aliases/main", map[string]string{"image_id": "missing"}, http.StatusNotFound)
	e.errorCode("one", http.MethodPut, "/v1/image-aliases/Main", map[string]string{"image_id": e.imageID}, http.StatusBadRequest)
	e.do("one", http.MethodPut, "/v1/image-aliases/main", map[string]string{"image_id": e.imageID}, http.StatusOK, nil)
	var list struct {
		Images []images.Image `json:"images"`
	}
	e.do("one", http.MethodGet, "/v1/images", nil, http.StatusOK, &list)
	if len(list.Images) != 1 || len(list.Images[0].Aliases) != 1 || list.Images[0].Aliases[0] != "main" {
		t.Fatalf("images = %+v", list.Images)
	}

	var d lifecycle.Defaults
	e.do("one", http.MethodGet, "/v1/defaults", nil, http.StatusOK, &d)
	if d.Image != "main" || d.VCPUs != 2 {
		t.Fatalf("built-in defaults = %+v", d)
	}
	e.errorCode("one", http.MethodPut, "/v1/defaults", map[string]any{"image": "main", "vcpus": 0, "memory_mib": 512, "disk_gib": 4}, http.StatusBadRequest)
	e.do("one", http.MethodPut, "/v1/defaults", lifecycle.Defaults{Image: "main", VCPUs: 1, MemoryMiB: 512, DiskGiB: 4}, http.StatusOK, nil)

	var acc lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", map[string]string{"name": "travel"}, http.StatusAccepted, &acc)
	e.runner.Wait()
	var got lifecycle.Environment
	e.do("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusOK, &got)
	if got.ImageID != e.imageID || got.VCPUs != 1 || got.MemoryMiB != 512 {
		t.Fatalf("environment = %+v", got)
	}
}

func TestSSHKeyRoutes(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	var keys struct {
		SSHKeys []sshkeys.Key `json:"ssh_keys"`
	}
	e.do("one", http.MethodGet, "/v1/ssh-keys", nil, http.StatusOK, &keys)
	if len(keys.SSHKeys) != 1 || keys.SSHKeys[0].Name != "laptop" || strings.Contains(keys.SSHKeys[0].PublicKey, " one") {
		t.Fatalf("keys = %+v", keys.SSHKeys)
	}
	var again sshkeys.Key
	e.do("one", http.MethodPost, "/v1/ssh-keys", map[string]string{"name": "other", "public_key": testKeys["one"]}, http.StatusOK, &again)
	if again.ID != keys.SSHKeys[0].ID {
		t.Fatalf("re-adding returned %+v", again)
	}
	e.errorCode("one", http.MethodPost, "/v1/ssh-keys", map[string]string{"name": "x", "public_key": `command="sh" ` + testKeys["one"]}, http.StatusBadRequest)
	e.errorCode("two", http.MethodDelete, "/v1/ssh-keys/"+again.ID, nil, http.StatusNotFound)
	e.do("one", http.MethodDelete, "/v1/ssh-keys/"+again.ID, nil, http.StatusNoContent, nil)
	if code := e.errorCode("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("create without keys: code = %s", code)
	}
}
