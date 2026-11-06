package aicli_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aicli "github.com/redSun64/ai-cli"
)

func TestHTTPPolicyIsolationAndNoPreviewTraffic(t *testing.T) {
	var requests, authCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-API-Key") != "fixture-credential" {
			t.Error("missing auth")
		}
		if r.Method == "POST" && r.URL.Path == "/write" && r.Header.Get("Idempotency-Key") != "job-1" {
			t.Error("missing idempotency key")
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	provider := aicli.AuthProviderFunc(func(context.Context, string, string) (http.Header, error) {
		authCalls.Add(1)
		return http.Header{"X-Api-Key": {"fixture-credential"}}, nil
	})
	var retained *aicli.Client
	start := make(chan struct{})
	r := runtimeFor(t, aicli.Config{HTTP: &aicli.HTTPConfig{BaseURL: server.URL, Auth: provider, ReadOnlyPostPaths: []string{"/search"}}, Commands: []aicli.Command{
		{Name: "write", Risk: aicli.WriteRisk(), Run: func(i aicli.Invocation) (any, error) {
			retained = i.HTTP
			<-start
			return nil, i.HTTP.Do(i.Context, aicli.Request{Method: "POST", Path: "/write", IdempotencyKey: "job-1"}, nil)
		}},
		{Name: "read", Risk: aicli.ReadRisk(), Run: func(i aicli.Invocation) (any, error) {
			return nil, i.HTTP.Do(i.Context, aicli.Request{Method: "POST", Path: "/write"}, nil)
		}},
		{Name: "search", Risk: aicli.ReadRisk(), Run: func(i aicli.Invocation) (any, error) {
			return nil, i.HTTP.Do(i.Context, aicli.Request{Method: "POST", Path: "/search"}, nil)
		}},
	}})
	_, err := r.Execute(context.Background(), []string{"write"}, nil, aicli.ExecuteOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Execute(context.Background(), []string{"write"}, nil, aicli.ExecuteOptions{})
	requireCode(t, err, "WRITE_APPROVAL_REQUIRED")
	if requests.Load() != 0 || authCalls.Load() != 0 {
		t.Fatal("preview/unapproved touched network or credentials")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := r.Execute(context.Background(), []string{"write"}, nil, aicli.ExecuteOptions{Confirmed: true})
		if err != nil {
			t.Error(err)
		}
	}()
	_, err = r.Execute(context.Background(), []string{"read"}, nil, aicli.ExecuteOptions{Confirmed: true})
	requireCode(t, err, "WRITE_NOT_ALLOWED")
	close(start)
	wg.Wait()
	_, err = r.Execute(context.Background(), []string{"search"}, nil, aicli.ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal(requests.Load())
	}
	err = retained.Do(context.Background(), aicli.Request{Method: "POST", Path: "/write"}, nil)
	requireCode(t, err, "EXECUTION_INACTIVE")
}

func TestHTTPBoundaries(t *testing.T) {
	var redirectHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, other.URL, 302)
		case "/error":
			w.WriteHeader(500)
			fmt.Fprint(w, "fixture-secret")
		case "/big":
			fmt.Fprint(w, strings.Repeat("X", 256))
		case "/invalid":
			fmt.Fprint(w, "invalid-json")
		case "/slow":
			<-r.Context().Done()
		case "/empty":
			w.WriteHeader(204)
		default:
			fmt.Fprint(w, `{"value":42}`)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name    string
		request aicli.Request
		code    string
		timeout time.Duration
	}{
		{"cross origin", aicli.Request{Path: other.URL}, "HTTP_INVALID_URL", time.Second},
		{"url credentials", aicli.Request{Path: strings.Replace(server.URL, "://", "://user:pass@", 1)}, "HTTP_INVALID_URL", time.Second},
		{"redirect", aicli.Request{Path: "/redirect"}, "HTTP_ERROR", time.Second},
		{"HTTP error", aicli.Request{Path: "/error"}, "HTTP_ERROR", time.Second},
		{"oversized", aicli.Request{Path: "/big"}, "HTTP_RESPONSE_TOO_LARGE", time.Second},
		{"invalid JSON", aicli.Request{Path: "/invalid"}, "RESPONSE_DESERIALIZATION_FAILED", time.Second},
		{"timeout", aicli.Request{Path: "/slow"}, "HTTP_IO_ERROR", 25 * time.Millisecond},
		{"auth injection", aicli.Request{Path: "/", Headers: http.Header{"authorization": {"fixture-secret"}}}, "HTTP_AUTH_HEADER_FORBIDDEN", time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runtimeFor(t, aicli.Config{HTTP: &aicli.HTTPConfig{BaseURL: server.URL, Timeout: tc.timeout, MaxResponseBytes: 128}, Commands: []aicli.Command{{Name: "read", Risk: aicli.ReadRisk(), Run: func(i aicli.Invocation) (any, error) { var out any; return nil, i.HTTP.Do(i.Context, tc.request, &out) }}}})
			_, err := r.Execute(context.Background(), []string{"read"}, nil, aicli.ExecuteOptions{})
			requireCode(t, err, tc.code)
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("response leaked credential")
			}
		})
	}
	if redirectHits.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestHTTPReadOnlyPOSTAndOriginValidation(t *testing.T) {
	for _, declaration := range []string{"//host/path", "https://host/path", "/search?x=1", "/a/../search", "/%73earch", "/search#fragment"} {
		_, err := aicli.New(aicli.Config{Name: "test", HTTP: &aicli.HTTPConfig{BaseURL: "https://example.com", ReadOnlyPostPaths: []string{declaration}}})
		requireCode(t, err, "HTTP_INVALID_READ_ONLY_POST_PATH")
	}
	for _, base := range []string{"", "ftp://example.com", "https://user:pass@example.com", "https://example.com?token=fixture"} {
		_, err := aicli.New(aicli.Config{Name: "test", HTTP: &aicli.HTTPConfig{BaseURL: base}})
		requireCode(t, err, "HTTP_INVALID_URL")
	}
}

func TestHTTPCancellationAndRawResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "raw response") }))
	defer server.Close()
	r := runtimeFor(t, aicli.Config{HTTP: &aicli.HTTPConfig{BaseURL: server.URL}, Commands: []aicli.Command{{Name: "read", Risk: aicli.ReadRisk(), Run: func(i aicli.Invocation) (any, error) {
		ctx, cancel := context.WithCancel(i.Context)
		cancel()
		var text string
		requireCode(t, i.HTTP.Do(ctx, aicli.Request{Path: "/"}, &text), "HTTP_IO_ERROR")
		if err := i.HTTP.Do(i.Context, aicli.Request{Path: "/"}, &text); err != nil {
			return nil, err
		}
		return text, nil
	}}}})
	data, err := r.Execute(context.Background(), []string{"read"}, nil, aicli.ExecuteOptions{})
	if err != nil || data != "raw response" || calls.Load() != 1 {
		t.Fatal(data, err, calls.Load())
	}
}
