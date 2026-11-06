package aicli

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
)

// CredentialStore is a replaceable, domain-scoped secret store. Implementations
// should use an OS keychain or an organization's secret service for persistence.
type CredentialStore interface {
	Get(context.Context, string, string) (string, error)
	Set(context.Context, string, string, string) error
	Delete(context.Context, string, string) error
}

// MemoryStore is a concurrency-safe, process-local store. It does not persist secrets.
type MemoryStore struct {
	mu     sync.RWMutex
	values map[string]map[string]string
}

func (s *MemoryStore) Get(_ context.Context, domain, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.values[domain][key], nil
}
func (s *MemoryStore) Set(_ context.Context, domain, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]map[string]string{}
	}
	if s.values[domain] == nil {
		s.values[domain] = map[string]string{}
	}
	s.values[domain][key] = value
	return nil
}
func (s *MemoryStore) Delete(_ context.Context, domain, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values[domain], key)
	return nil
}

// AuthProvider generates request-specific headers. The SDK calls it after URL
// and execution-policy checks. Implementations must honor context cancellation.
type AuthProvider interface {
	Headers(context.Context, string, string) (http.Header, error)
}

// AuthProviderFunc adapts a function to AuthProvider (arguments: context, method, URL).
type AuthProviderFunc func(context.Context, string, string) (http.Header, error)

func (f AuthProviderFunc) Headers(ctx context.Context, method, url string) (http.Header, error) {
	return f(ctx, method, url)
}

// AuthConfig configures none, bearer or api-key authentication. LookupEnv defaults
// to os.Getenv. A nonempty environment value takes precedence over Store.
type AuthConfig struct {
	DomainID      string
	Type          string
	CredentialKey string
	Header        string
	Prefix        string
	Required      bool
	Store         CredentialStore
	LookupEnv     func(string) string
}

// AuthManager resolves credentials without exposing them in status output.
type AuthManager struct{ config AuthConfig }

var domainID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func defaultLookupEnv(key string) string { return os.Getenv(key) }

// NewAuth validates built-in authentication. Custom schemes implement AuthProvider
// and are supplied through HTTPConfig.Auth instead.
func NewAuth(config AuthConfig) (*AuthManager, error) {
	if !domainID.MatchString(config.DomainID) {
		return nil, failure("AUTH_CONFIG_INVALID", "A valid domain ID is required.")
	}
	if config.Type != "none" && config.Type != "bearer" && config.Type != "api-key" {
		return nil, failure("AUTH_TYPE_UNSUPPORTED", "Use an AuthProvider for custom authentication.")
	}
	if config.Type != "none" && config.CredentialKey == "" {
		return nil, failure("AUTH_CONFIG_INVALID", "CredentialKey is required.")
	}
	if config.Header == "" {
		config.Header = "Authorization"
	}
	if !headerName.MatchString(config.Header) || strings.ContainsAny(config.Prefix, "\r\n") {
		return nil, failure("AUTH_CONFIG_INVALID", "Invalid authentication header configuration.")
	}
	if config.Type == "bearer" && config.Prefix == "" {
		config.Prefix = "Bearer "
	}
	if config.LookupEnv == nil {
		config.LookupEnv = os.Getenv
	}
	return &AuthManager{config}, nil
}

func (a *AuthManager) credential(ctx context.Context) (string, error) {
	if a.config.Type == "none" {
		return "", nil
	}
	if value := a.config.LookupEnv(a.config.CredentialKey); value != "" {
		return value, nil
	}
	if a.config.Store == nil {
		return "", nil
	}
	value, err := a.config.Store.Get(ctx, a.config.DomainID, a.config.CredentialKey)
	if err != nil {
		return "", failure("CREDENTIAL_STORE_FAILED", "Could not read credential store.")
	}
	return value, nil
}

// AuthStatus reports local credential availability, not server-side validity.
type AuthStatus struct {
	Domain        string `json:"domain"`
	Type          string `json:"type"`
	Authenticated bool   `json:"authenticated"`
	Message       string `json:"message"`
}

// Status checks availability without returning credential values.
func (a *AuthManager) Status(ctx context.Context) (AuthStatus, error) {
	value, err := a.credential(ctx)
	if err != nil {
		return AuthStatus{}, err
	}
	ok := a.config.Type == "none" || value != ""
	message := "credential unavailable"
	if ok {
		message = "credential available"
	}
	return AuthStatus{a.config.DomainID, a.config.Type, ok, message}, nil
}

// Login stores an existing credential using the caller's store. This method does
// not acquire tokens and the SDK never stores them in plaintext files.
func (a *AuthManager) Login(ctx context.Context, secret string) error {
	if a.config.Type == "none" || a.config.Store == nil || secret == "" {
		return failure("AUTH_CONFIG_INVALID", "Login requires a non-empty credential and a configured store.")
	}
	if err := a.config.Store.Set(ctx, a.config.DomainID, a.config.CredentialKey, secret); err != nil {
		return failure("CREDENTIAL_STORE_FAILED", "Could not save credential.")
	}
	return nil
}

// Logout deletes only this domain/key from Store. Environment credentials remain
// managed by the environment's owner.
func (a *AuthManager) Logout(ctx context.Context) error {
	if a.config.Store == nil || a.config.Type == "none" {
		return nil
	}
	if err := a.config.Store.Delete(ctx, a.config.DomainID, a.config.CredentialKey); err != nil {
		return failure("CREDENTIAL_STORE_FAILED", "Could not delete credential.")
	}
	return nil
}

// Headers implements AuthProvider and injects the resolved credential.
func (a *AuthManager) Headers(ctx context.Context, method, url string) (http.Header, error) {
	headers := http.Header{}
	if a.config.Type == "none" {
		return headers, nil
	}
	value, err := a.credential(ctx)
	if err != nil {
		return nil, err
	}
	if value == "" {
		if a.config.Required {
			return nil, failure("AUTH_LOGIN_REQUIRED", "Credential is required.")
		}
		return headers, nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return nil, failure("AUTH_CREDENTIAL_INVALID", "Credential contains invalid characters.")
	}
	headers.Set(a.config.Header, a.config.Prefix+value)
	return headers, nil
}
