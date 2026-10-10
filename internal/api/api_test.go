package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

type fakeGateway struct{}

func (fakeGateway) Endpoint(_ context.Context, id string) (netip.Addr, error) {
	return netip.MustParseAddr("172.20.0.6"), nil
}
func (fakeGateway) HostKey(_ context.Context, diskID string) (string, error) {
	return "ssh-ed25519 AAAAhost", nil
}

type env struct {
	t         *testing.T
	db        *sql.DB
	dialed    chan string
	srv       *httptest.Server
	tailnet   *httptest.Server
	authStore *auth.Store
	runner    *lifecycle.Runner
	tokens    map[string]string
	imageID   string
	stage     func(name string)
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
		Gateway:      fakeGateway{},
		Capacity:     capacity,
		PollInterval: 10 * time.Millisecond,
	}
	dialed := make(chan string, 10)
	s.DialGuest = func(_ context.Context, address string) (net.Conn, error) {
		dialed <- address
		guest, client := net.Pipe()
		go func() { io.Copy(guest, guest); guest.Close() }()
		return client, nil
	}
	h := s.Handler()
	srv := httptest.NewServer(h)
	tailnet := httptest.NewUnstartedServer(h)
	tailnet.Config.ConnContext = ViaTailscale
	tailnet.Start()
	t.Cleanup(func() { srv.Close(); tailnet.Close(); cancel(); runner.Wait(); db.Close() })

	e := &env{t: t, db: db, dialed: dialed, srv: srv, tailnet: tailnet, authStore: authStore, runner: runner, tokens: map[string]string{}}
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

func TestTokenRoutes(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	code, err := e.authStore.Bootstrap(context.Background(), "one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]string
	e.do("", http.MethodPost, "/v1/login", map[string]string{"code": code, "name": "tablet"}, http.StatusOK, &login)
	e.tokens["tablet"] = login["token"]

	var list struct {
		Tokens []auth.Token `json:"tokens"`
	}
	e.do("tablet", http.MethodGet, "/v1/tokens", nil, http.StatusOK, &list)
	if len(list.Tokens) != 2 || list.Tokens[0].Name != "unnamed" || list.Tokens[1].Name != "tablet" || !list.Tokens[1].Current || list.Tokens[0].Current {
		t.Fatalf("tokens = %+v", list.Tokens)
	}
	e.errorCode("two", http.MethodDelete, "/v1/tokens/"+list.Tokens[1].ID, nil, http.StatusNotFound)
	e.do("one", http.MethodDelete, "/v1/tokens/"+list.Tokens[1].ID, nil, http.StatusNoContent, nil)
	e.errorCode("tablet", http.MethodGet, "/v1/environments", nil, http.StatusUnauthorized)
	e.do("one", http.MethodGet, "/v1/environments", nil, http.StatusOK, nil)
}

func TestFailedLoginsAreRateLimited(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	for i := 0; i < 9; i++ {
		e.errorCode("", http.MethodPost, "/v1/login", map[string]string{"code": "WRONG"}, http.StatusUnauthorized)
	}
	resp := e.request("", http.MethodPost, "/v1/login", map[string]string{"code": "WRONG"}, map[string]string{"X-Forwarded-For": "100.64.0.9"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tenth failure: status %d", resp.StatusCode)
	}
	code, _ := e.authStore.Bootstrap(context.Background(), "one", time.Minute)
	var resp429 struct {
		Error apiError `json:"error"`
	}
	e.do("", http.MethodPost, "/v1/login", map[string]string{"code": code}, http.StatusTooManyRequests, &resp429)
	if resp429.Error.Code != "rate_limited" || !resp429.Error.Retryable {
		t.Fatalf("error = %+v", resp429.Error)
	}
}

func (e *env) tunnel(who, environmentID string, header string) (*http.Response, net.Conn, *bufio.Reader) {
	e.t.Helper()
	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET /v1/environments/%s/ssh HTTP/1.1\r\nHost: boxface\r\nAuthorization: Bearer %s\r\n%s\r\n", environmentID, e.tokens[who], header)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, conn, br
}

const upgradeHeader = "Connection: Upgrade\r\nUpgrade: boxface-ssh\r\n"

func TestConnectionAndSSHTunnel(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	var acc lifecycle.Accepted
	e.do("one", http.MethodPost, "/v1/environments", e.createRequest("travel"), http.StatusAccepted, &acc)
	e.runner.Wait()

	var conn map[string]string
	e.do("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID+"/connection", nil, http.StatusOK, &conn)
	if conn["user"] != "dev" || conn["host_key"] != "ssh-ed25519 AAAAhost" || conn["environment_id"] != acc.EnvironmentID {
		t.Fatalf("connection = %+v", conn)
	}
	e.errorCode("two", http.MethodGet, "/v1/environments/"+acc.EnvironmentID+"/connection", nil, http.StatusNotFound)

	resp, c, br := e.tunnel("one", acc.EnvironmentID, upgradeHeader)
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "boxface-ssh" {
		t.Fatalf("status %d, header %v", resp.StatusCode, resp.Header)
	}
	if got := <-e.dialed; got != "172.20.0.6:22" {
		t.Fatalf("dialed %s", got)
	}
	io.WriteString(c, "SSH-2.0-test\r\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "SSH-2.0-test\r\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
	var events int
	e.db.QueryRow(`SELECT count(*) FROM auth_events WHERE action = 'environment.connect' AND target_id = ?`, acc.EnvironmentID).Scan(&events)
	if events != 1 {
		t.Fatalf("connect events = %d", events)
	}

	for name, tc := range map[string]struct {
		who, header string
		status      int
		code        string
	}{
		"another owner":     {"two", upgradeHeader, http.StatusNotFound, "not_found"},
		"no upgrade header": {"one", "", http.StatusBadRequest, "invalid_request"},
		"wrong protocol":    {"one", "Connection: Upgrade\r\nUpgrade: websocket\r\n", http.StatusBadRequest, "invalid_request"},
	} {
		resp, _, _ := e.tunnel(tc.who, acc.EnvironmentID, tc.header)
		var body struct {
			Error apiError `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		if resp.StatusCode != tc.status || body.Error.Code != tc.code {
			t.Fatalf("%s: status %d, error %+v", name, resp.StatusCode, body.Error)
		}
	}

	e.do("one", http.MethodPost, "/v1/environments/"+acc.EnvironmentID+"/stop", nil, http.StatusAccepted, nil)
	e.runner.Wait()
	resp, _, _ = e.tunnel("one", acc.EnvironmentID, upgradeHeader)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stopped: status %d", resp.StatusCode)
	}
	e.do("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID+"/connection", nil, http.StatusOK, nil)

	e.do("one", http.MethodDelete, "/v1/environments/"+acc.EnvironmentID, nil, http.StatusAccepted, nil)
	e.runner.Wait()
	resp, _, _ = e.tunnel("one", acc.EnvironmentID, upgradeHeader)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleted: status %d", resp.StatusCode)
	}
	if code := e.errorCode("one", http.MethodGet, "/v1/environments/"+acc.EnvironmentID+"/connection", nil, http.StatusConflict); code != "environment_deleted" {
		t.Fatalf("deleted connection: code = %s", code)
	}
	select {
	case got := <-e.dialed:
		t.Fatalf("refused tunnel dialed %s", got)
	default:
	}
}

func (e *env) tailnetRequest(who, method, path, login string, body any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.tailnet.URL+path, strings.NewReader(string(b)))
	if token := e.tokens[who]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if login != "" {
		req.Header.Set("Tailscale-User-Login", login)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestTailscaleIdentityLogin(t *testing.T) {
	e := setup(t, lifecycle.Capacity{})
	noCode := map[string]string{"name": "tablet"}

	for i := 0; i < 12; i++ {
		e.errorCode("", http.MethodPost, "/v1/login", noCode, http.StatusUnauthorized)
	}
	resp := e.request("", http.MethodPost, "/v1/login", noCode, map[string]string{"Tailscale-User-Login": "onat@github"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("identity header off the tailnet socket: status %d", resp.StatusCode)
	}
	if status, _ := e.tailnetRequest("", http.MethodPost, "/v1/login", "onat@github", noCode); status != http.StatusUnauthorized {
		t.Fatalf("unbound login: status %d", status)
	}

	if code := e.errorCode("one", http.MethodPost, "/v1/identities", map[string]string{"provider": "tailscale"}, http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("bind off the tailnet socket: code = %s", code)
	}
	if status, _ := e.tailnetRequest("one", http.MethodPost, "/v1/identities", "", map[string]string{"provider": "tailscale"}); status != http.StatusBadRequest {
		t.Fatalf("bind from a tagged device: status %d", status)
	}
	status, ident := e.tailnetRequest("one", http.MethodPost, "/v1/identities", "onat@github", map[string]string{"provider": "tailscale"})
	if status != http.StatusCreated || ident["subject"] != "onat@github" {
		t.Fatalf("bind: status %d, %v", status, ident)
	}
	if status, _ := e.tailnetRequest("one", http.MethodPost, "/v1/identities", "onat@github", map[string]string{"provider": "tailscale"}); status != http.StatusOK {
		t.Fatalf("rebind: status %d", status)
	}
	if status, body := e.tailnetRequest("two", http.MethodPost, "/v1/identities", "onat@github", map[string]string{"provider": "tailscale"}); status != http.StatusConflict {
		t.Fatalf("bind by another owner: status %d, %v", status, body)
	}

	status, login := e.tailnetRequest("", http.MethodPost, "/v1/login", "onat@github", noCode)
	if status != http.StatusOK || login["token"] == "" {
		t.Fatalf("identity login: status %d, %v", status, login)
	}
	e.tokens["tablet"] = login["token"].(string)
	var tokens struct {
		Tokens []auth.Token `json:"tokens"`
	}
	e.do("tablet", http.MethodGet, "/v1/tokens", nil, http.StatusOK, &tokens)
	if len(tokens.Tokens) != 2 || tokens.Tokens[1].Name != "tablet" {
		t.Fatalf("owner one tokens = %+v", tokens.Tokens)
	}
	if status, _ := e.tailnetRequest("", http.MethodPost, "/v1/login", "", noCode); status != http.StatusUnauthorized {
		t.Fatalf("tailnet login without identity: status %d", status)
	}

	var list struct {
		Identities []auth.Identity `json:"identities"`
	}
	e.do("one", http.MethodGet, "/v1/identities", nil, http.StatusOK, &list)
	if len(list.Identities) != 1 {
		t.Fatalf("identities = %+v", list.Identities)
	}
	e.errorCode("two", http.MethodDelete, "/v1/identities/"+list.Identities[0].ID, nil, http.StatusNotFound)
	e.do("one", http.MethodDelete, "/v1/identities/"+list.Identities[0].ID, nil, http.StatusNoContent, nil)
	if status, _ := e.tailnetRequest("", http.MethodPost, "/v1/login", "onat@github", noCode); status != http.StatusUnauthorized {
		t.Fatalf("login after unbind: status %d", status)
	}
	code, _ := e.authStore.Bootstrap(context.Background(), "one", time.Minute)
	e.do("", http.MethodPost, "/v1/login", map[string]string{"code": code}, http.StatusOK, nil)
}
