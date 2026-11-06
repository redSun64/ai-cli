package aicli_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	aicli "github.com/redSun64/ai-cli"
)

func TestAuthPrecedenceAndLifecycle(t *testing.T) {
	store := &aicli.MemoryStore{}
	ctx := context.Background()
	if err := store.Set(ctx, "other", "TOKEN", "other-fixture"); err != nil {
		t.Fatal(err)
	}
	env := ""
	auth, err := aicli.NewAuth(aicli.AuthConfig{DomainID: "test", Type: "bearer", CredentialKey: "TOKEN", Required: true, Store: store, LookupEnv: func(string) string { return env }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Headers(ctx, "GET", "https://example.com")
	requireCode(t, err, "AUTH_LOGIN_REQUIRED")
	if err := auth.Login(ctx, "stored-fixture"); err != nil {
		t.Fatal(err)
	}
	env = "env-fixture"
	headers, err := auth.Headers(ctx, "GET", "https://example.com")
	if err != nil || headers.Get("Authorization") != "Bearer env-fixture" {
		t.Fatal(headers, err)
	}
	if err := auth.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := auth.Status(ctx)
	if err != nil || !status.Authenticated {
		t.Fatal(status, err)
	}
	env = ""
	status, err = auth.Status(ctx)
	if err != nil || status.Authenticated {
		t.Fatal(status, err)
	}
	other, _ := store.Get(ctx, "other", "TOKEN")
	if other != "other-fixture" {
		t.Fatal("removed other domain credential")
	}
	keyAuth, err := aicli.NewAuth(aicli.AuthConfig{DomainID: "test", Type: "api-key", CredentialKey: "TOKEN", Header: "X-API-Key", LookupEnv: func(string) string { return "key-fixture" }})
	if err != nil {
		t.Fatal(err)
	}
	headers, err = keyAuth.Headers(ctx, "GET", "https://example.com")
	if err != nil || headers.Get("X-API-Key") != "key-fixture" {
		t.Fatal(headers, err)
	}
}

func TestAuthCLI(t *testing.T) {
	store := &aicli.MemoryStore{}
	auth, err := aicli.NewAuth(aicli.AuthConfig{DomainID: "test", Type: "bearer", CredentialKey: "TOKEN", Required: true, Store: store, LookupEnv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	r := runtimeFor(t, aicli.Config{Auth: auth})
	for _, tc := range []struct {
		argv    []string
		code    int
		options aicli.CLIOptions
	}{
		{[]string{"auth", "status"}, 3, aicli.CLIOptions{}},
		{[]string{"auth", "login"}, 2, aicli.CLIOptions{}},
		{[]string{"auth", "login"}, 0, aicli.CLIOptions{ReadSecret: func(context.Context) (string, error) { return "login-fixture", nil }}},
		{[]string{"auth", "status"}, 0, aicli.CLIOptions{}},
		{[]string{"auth", "logout", "--dry-run"}, 0, aicli.CLIOptions{}},
		{[]string{"auth", "logout"}, 2, aicli.CLIOptions{}},
		{[]string{"auth", "logout", "-y"}, 0, aicli.CLIOptions{}},
		{[]string{"auth", "status"}, 3, aicli.CLIOptions{}},
	} {
		var out, errOut bytes.Buffer
		code := r.RunCLI(context.Background(), tc.argv, &out, &errOut, tc.options)
		if code != tc.code || strings.Contains(out.String()+errOut.String(), "login-fixture") {
			t.Fatalf("%v: code %d %s %s", tc.argv, code, &out, &errOut)
		}
	}
}

type failingAuth struct{}

func (failingAuth) Headers(context.Context, string, string) (http.Header, error) {
	return nil, &aicli.Error{Code: "CUSTOM_AUTH_ERROR", Message: "fixture-secret"}
}

func TestCustomProviderFailureIsSanitized(t *testing.T) {
	r := runtimeFor(t, aicli.Config{HTTP: &aicli.HTTPConfig{BaseURL: "https://example.com", Auth: failingAuth{}}, Commands: []aicli.Command{{Name: "read", Risk: aicli.ReadRisk(), Run: func(i aicli.Invocation) (any, error) { return nil, i.HTTP.Do(i.Context, aicli.Request{Path: "/"}, nil) }}}})
	_, err := r.Execute(context.Background(), []string{"read"}, nil, aicli.ExecuteOptions{})
	requireCode(t, err, "AUTH_PROVIDER_FAILED")
	if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal(err)
	}
}
