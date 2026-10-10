package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/sshkeys"
)

type Error struct {
	Status    int
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

type Client struct {
	base       string
	token      string
	http       *http.Client
	RetryDelay time.Duration
}

func New(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{}, RetryDelay: time.Second}
}

const attempts = 3

func (c *Client) send(ctx context.Context, method, path string, body any, header map[string]string) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.RetryDelay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || path == "/v1/login" {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("server unreachable after %d attempts: %w", attempts, lastErr)
}

func (c *Client) do(ctx context.Context, method, path string, body any, header map[string]string, out any) error {
	resp, err := c.send(ctx, method, path, body, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error Error `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Error.Code == "" {
			return &Error{Status: resp.StatusCode, Code: "http_error", Message: resp.Status}
		}
		e.Error.Status = resp.StatusCode
		return &e.Error
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func key(idempotencyKey string) map[string]string {
	if idempotencyKey == "" {
		return nil
	}
	return map[string]string{"Idempotency-Key": idempotencyKey}
}

func (c *Client) Login(ctx context.Context, code string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/login", map[string]string{"code": code}, nil, &out)
	return out.Token, err
}

func (c *Client) ImportImage(ctx context.Context, source string) (images.Image, error) {
	var img images.Image
	err := c.do(ctx, http.MethodPost, "/v1/images", map[string]string{"source": source}, nil, &img)
	return img, err
}

func (c *Client) Images(ctx context.Context) ([]images.Image, error) {
	var out struct {
		Images []images.Image `json:"images"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/images", nil, nil, &out)
	return out.Images, err
}

func (c *Client) SetImageAlias(ctx context.Context, name, imageID string) error {
	return c.do(ctx, http.MethodPut, "/v1/image-aliases/"+url.PathEscape(name), map[string]string{"image_id": imageID}, nil, nil)
}

func (c *Client) AddSSHKey(ctx context.Context, name, publicKey string) (sshkeys.Key, error) {
	var k sshkeys.Key
	err := c.do(ctx, http.MethodPost, "/v1/ssh-keys", map[string]string{"name": name, "public_key": publicKey}, nil, &k)
	return k, err
}

func (c *Client) Create(ctx context.Context, idempotencyKey string, req lifecycle.CreateRequest) (lifecycle.Accepted, error) {
	var acc lifecycle.Accepted
	err := c.do(ctx, http.MethodPost, "/v1/environments", req, key(idempotencyKey), &acc)
	return acc, err
}

func (c *Client) Environments(ctx context.Context) ([]lifecycle.Environment, error) {
	var out struct {
		Environments []lifecycle.Environment `json:"environments"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/environments", nil, nil, &out)
	return out.Environments, err
}

func (c *Client) Environment(ctx context.Context, id string) (lifecycle.Environment, error) {
	var env lifecycle.Environment
	err := c.do(ctx, http.MethodGet, "/v1/environments/"+url.PathEscape(id), nil, nil, &env)
	return env, err
}

func (c *Client) Start(ctx context.Context, idempotencyKey, id string) (lifecycle.Accepted, error) {
	var acc lifecycle.Accepted
	err := c.do(ctx, http.MethodPost, "/v1/environments/"+url.PathEscape(id)+"/start", nil, key(idempotencyKey), &acc)
	return acc, err
}

func (c *Client) Stop(ctx context.Context, idempotencyKey, id string) (lifecycle.Accepted, error) {
	var acc lifecycle.Accepted
	err := c.do(ctx, http.MethodPost, "/v1/environments/"+url.PathEscape(id)+"/stop", nil, key(idempotencyKey), &acc)
	return acc, err
}

func (c *Client) Delete(ctx context.Context, idempotencyKey, id string, purge bool) (lifecycle.Accepted, error) {
	var acc lifecycle.Accepted
	path := "/v1/environments/" + url.PathEscape(id)
	if purge {
		path += "?purge=true"
	}
	err := c.do(ctx, http.MethodDelete, path, nil, key(idempotencyKey), &acc)
	return acc, err
}

func (c *Client) Operation(ctx context.Context, id string) (lifecycle.Operation, error) {
	var op lifecycle.Operation
	err := c.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id), nil, nil, &op)
	return op, err
}

func (c *Client) Follow(ctx context.Context, id string, onStep func(step string)) (lifecycle.Operation, error) {
	last := 0
	for {
		header := map[string]string{"Accept": "text/event-stream"}
		if last > 0 {
			header["Last-Event-ID"] = strconv.Itoa(last)
		}
		resp, err := c.send(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id)+"/events", nil, header)
		if err != nil {
			return lifecycle.Operation{}, err
		}
		if resp.StatusCode >= 300 {
			resp.Body.Close()
			_, err := c.Operation(ctx, id)
			if err == nil {
				err = fmt.Errorf("event stream returned %s", resp.Status)
			}
			return lifecycle.Operation{}, err
		}
		op, done := readEvents(resp.Body, &last, onStep)
		resp.Body.Close()
		if done {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return lifecycle.Operation{}, ctx.Err()
		case <-time.After(c.RetryDelay):
		}
	}
}

func readEvents(body io.Reader, last *int, onStep func(string)) (lifecycle.Operation, bool) {
	scanner := bufio.NewScanner(body)
	var id int
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id, _ = strconv.Atoi(strings.TrimPrefix(line, "id: "))
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			*last = id
			switch event {
			case "step":
				var s struct {
					Step string `json:"step"`
				}
				json.Unmarshal([]byte(data), &s)
				if onStep != nil {
					onStep(s.Step)
				}
			case "state":
				var op lifecycle.Operation
				if err := json.Unmarshal([]byte(data), &op); err != nil {
					return lifecycle.Operation{}, false
				}
				return op, true
			}
		}
	}
	return lifecycle.Operation{}, false
}
