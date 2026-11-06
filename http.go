package aicli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// HTTPConfig declares one service origin. BaseURLEnv, when nonempty, overrides
// BaseURL through LookupEnv (default os.Getenv). Redirects and cookie jars are
// disabled; the SDK does not retry failed requests.
type HTTPConfig struct {
	BaseURL          string
	BaseURLEnv       string
	LookupEnv        func(string) string
	DomainID         string
	Auth             AuthProvider
	Timeout          time.Duration
	MaxResponseBytes int64
	// Transport supports custom TLS/proxy configuration and test transports.
	// It must be concurrency-safe and must not implement automatic write retries.
	Transport http.RoundTripper
	// ReadOnlyPostPaths lists exact canonical URL pathnames, including any base
	// path, whose POST semantics have been verified to be read-only by the owner.
	ReadOnlyPostPaths []string
}

type httpSettings struct {
	base     *url.URL
	auth     AuthProvider
	client   *http.Client
	timeout  time.Duration
	maxBytes int64
	readPost map[string]bool
}

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func newHTTPSettings(config HTTPConfig, auth *AuthManager) (*httpSettings, error) {
	lookup := config.LookupEnv
	if lookup == nil {
		lookup = defaultLookupEnv
	}
	if config.BaseURLEnv != "" {
		if value := lookup(config.BaseURLEnv); value != "" {
			config.BaseURL = value
		}
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, failure("HTTP_INVALID_URL", "Configure an HTTP(S) base URL without credentials, query or fragment.")
	}
	if config.Timeout < 0 || config.MaxResponseBytes < 0 {
		return nil, failure("CONFIG_INVALID", "HTTP bounds must be positive.")
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 8 << 20
	}
	if config.MaxResponseBytes > 1<<30 {
		return nil, failure("CONFIG_INVALID", "Response limit must not exceed 1 GiB.")
	}
	provider := config.Auth
	if provider == nil && auth != nil {
		provider = auth
	}
	s := &httpSettings{base: base, auth: provider, timeout: config.Timeout, maxBytes: config.MaxResponseBytes, readPost: map[string]bool{},
		client: &http.Client{Transport: config.Transport, Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, declaration := range config.ReadOnlyPostPaths {
		u, err := url.Parse(declaration)
		if err != nil || !strings.HasPrefix(declaration, "/") || strings.HasPrefix(declaration, "//") || strings.Contains(declaration, "\\") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || path.Clean(u.Path) != declaration {
			return nil, failure("HTTP_INVALID_READ_ONLY_POST_PATH", "Read-only POST declarations must be canonical pathnames.")
		}
		s.readPost[declaration] = true
	}
	return s, nil
}

// Client executes authenticated HTTP calls only within its creating invocation.
// Obtain it from Invocation.HTTP; a zero Client returns a structured error.
// Clients retained after handler return cannot be used to issue more requests.
type Client struct {
	settings  *httpSettings
	execution *execution
}

// Request describes a single JSON request. Authentication belongs to AuthProvider;
// Authorization and an auth provider's header names cannot be overridden here.
type Request struct {
	Method         string
	Path           string
	Query          url.Values
	Headers        http.Header
	Body           any
	IdempotencyKey string
}

// Do sends a request and decodes JSON into out. Pass nil to discard the bounded
// response, or *[]byte / *string for raw responses. Non-2xx bodies are never
// included in errors. A failed write may have taken effect remotely.
func (c *Client) Do(ctx context.Context, request Request, out any) error {
	if c == nil || c.settings == nil {
		return failure("HTTP_CLIENT_UNCONFIGURED", "Configure HTTP on the Runtime.")
	}
	if c.execution == nil || !c.execution.active.Load() || c.execution.ctx.Err() != nil {
		return failure("EXECUTION_INACTIVE", "HTTP requires an active command execution.")
	}
	s := c.settings
	relative, err := url.Parse(request.Path)
	if err != nil || strings.Contains(request.Path, "\\") {
		return failure("HTTP_INVALID_URL", "Invalid request URL.")
	}
	target := s.base.ResolveReference(relative)
	if !strings.EqualFold(target.Scheme, s.base.Scheme) || !strings.EqualFold(target.Host, s.base.Host) || target.User != nil || target.Fragment != "" {
		return failure("HTTP_INVALID_URL", "Request must remain on the configured origin.")
	}
	method := strings.ToUpper(request.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !headerName.MatchString(method) {
		return failure("HTTP_INVALID_METHOD", "Invalid request method.")
	}
	read := method == "GET" || method == "HEAD" || method == "OPTIONS"
	if method == "POST" && s.readPost[target.Path] && target.RawPath == "" {
		read = true
	}
	if !read && !c.execution.write {
		return failure("WRITE_NOT_ALLOWED", "A READ command cannot issue a write request.")
	}
	query := target.Query()
	for key, values := range request.Query {
		query[key] = append([]string{}, values...)
	}
	target.RawQuery = query.Encode()
	headers := request.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	for name := range headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Host") || strings.EqualFold(name, "Cookie") {
			return failure("HTTP_AUTH_HEADER_FORBIDDEN", "Authentication must be supplied by an auth provider.")
		}
	}
	// Both caller cancellation and Runtime cancellation bound provider + transport.
	callCtx, cancel := context.WithTimeout(c.execution.ctx, s.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if ctx.Err() != nil {
		cancel()
	}
	if s.auth != nil {
		authHeaders, err := s.auth.Headers(callCtx, method, target.String())
		if err != nil {
			// Provider errors may contain tokens; preserve only a safe SDK failure.
			if e, ok := err.(*Error); ok && (e.Code == "AUTH_LOGIN_REQUIRED" || e.Code == "CREDENTIAL_STORE_FAILED" || e.Code == "AUTH_CREDENTIAL_INVALID") {
				return failure(e.Code, "Authentication could not be resolved.")
			}
			return failure("AUTH_PROVIDER_FAILED", "Authentication provider failed.")
		}
		for name, values := range authHeaders {
			for supplied := range headers {
				if strings.EqualFold(supplied, name) {
					return failure("HTTP_AUTH_HEADER_FORBIDDEN", "Authentication headers cannot be overridden.")
				}
			}
			headers[http.CanonicalHeaderKey(name)] = append([]string{}, values...)
		}
	}
	var body io.Reader
	if request.Body != nil {
		encoded, err := json.Marshal(request.Body)
		if err != nil {
			return failure("REQUEST_SERIALIZATION_FAILED", "Request body could not be encoded as JSON.")
		}
		body = bytes.NewReader(encoded)
		headers.Set("Content-Type", "application/json")
	}
	if request.IdempotencyKey != "" {
		headers.Set("Idempotency-Key", request.IdempotencyKey)
	}
	for name, values := range headers {
		if !headerName.MatchString(name) {
			return failure("HTTP_INVALID_HEADER", "Invalid request header.")
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				return failure("HTTP_INVALID_HEADER", "Invalid request header.")
			}
		}
	}
	req, err := http.NewRequestWithContext(callCtx, method, target.String(), body)
	if err != nil {
		return failure("HTTP_INVALID_REQUEST", "Could not construct HTTP request.")
	}
	req.Header = headers
	// net/http may replay requests carrying Idempotency-Key after a stale
	// connection failure. A non-replayable body also covers bodyless writes.
	// SDK write attempts must leave retry/recovery decisions to the workflow.
	if !read {
		req.GetBody = nil
		if req.Body == nil || req.Body == http.NoBody {
			req.Body = io.NopCloser(bytes.NewReader(nil))
		}
	}
	response, err := s.client.Do(req)
	if err != nil {
		return failure("HTTP_IO_ERROR", "HTTP request failed; remote effect may be unknown.")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Code: "HTTP_ERROR", Message: "Service returned an unsuccessful HTTP status.", Details: map[string]any{"status": response.StatusCode}}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, s.maxBytes+1))
	if err != nil {
		return failure("HTTP_IO_ERROR", "Response could not be read; remote effect may be unknown.")
	}
	if int64(len(data)) > s.maxBytes {
		return failure("HTTP_RESPONSE_TOO_LARGE", "Response exceeds the configured size limit.")
	}
	if out == nil {
		return nil
	}
	switch dest := out.(type) {
	case *[]byte:
		*dest = data
		return nil
	case *string:
		*dest = string(data)
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if json.Unmarshal(data, out) != nil {
		return failure("RESPONSE_DESERIALIZATION_FAILED", "Response was not valid JSON for the destination type.")
	}
	return nil
}
