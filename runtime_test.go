package aicli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	aicli "github.com/redSun64/ai-cli"
)

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *aicli.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func runtimeFor(t *testing.T, config aicli.Config) *aicli.Runtime {
	t.Helper()
	if config.Name == "" {
		config.Name = "test-cli"
	}
	r, err := aicli.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func noEnv(string) string { return "" }

func TestPreviewApprovalAndValidation(t *testing.T) {
	var calls atomic.Int32
	r := runtimeFor(t, aicli.Config{Commands: []aicli.Command{{Name: "account", Children: []aicli.Command{{Name: "+create", Risk: aicli.WriteRisk(),
		Options: []aicli.Option{{Name: "--name", Type: aicli.String, Required: true}, {Name: "--api-token", Type: aicli.String, Default: "fixture-secret"}, {Name: "--count", Type: aicli.Number, Default: 2}},
		Run:     func(i aicli.Invocation) (any, error) { calls.Add(1); return i.Args, nil }}}}}})
	path := []string{"account", "+create"}
	data, err := r.Execute(context.Background(), path, aicli.Arguments{"--name": "Example"}, aicli.ExecuteOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	plan := data.(aicli.Plan)
	if plan.Arguments["--api-token"] != "[REDACTED]" || plan.Arguments.Number("--count") != 2 {
		t.Fatal(plan)
	}
	_, err = r.Execute(context.Background(), path, aicli.Arguments{"--name": "Example"}, aicli.ExecuteOptions{})
	requireCode(t, err, "WRITE_APPROVAL_REQUIRED")
	for _, input := range []aicli.Arguments{{}, {"--name": 42}, {"--name": "X", "--unexpected": true}, {"--name": "X", "--count": math.Inf(1)}} {
		_, err := r.Execute(context.Background(), path, input, aicli.ExecuteOptions{DryRun: true, Confirmed: true})
		requireCode(t, err, "INVALID_ARGUMENT")
	}
	if calls.Load() != 0 {
		t.Fatal("preview/invalid/unapproved invoked handler")
	}
	data, err = r.Execute(context.Background(), path, aicli.Arguments{"--name": "Example"}, aicli.ExecuteOptions{Confirmed: true})
	if err != nil || calls.Load() != 1 || data.(aicli.Arguments).String("--api-token") != "fixture-secret" {
		t.Fatal(data, err)
	}
	schema, err := r.Schema(path...)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(schema)
	if strings.Contains(string(encoded), "fixture-secret") || !schema.Options[1].Sensitive || schema.Options[1].DefaultValue != nil {
		t.Fatal(string(encoded))
	}
}

func TestConfigurationSnapshotAndInvalidTrees(t *testing.T) {
	read := aicli.Command{Name: "read", Risk: aicli.ReadRisk(), Options: []aicli.Option{{Name: "--name", Type: aicli.String, Default: "original"}}, Run: func(i aicli.Invocation) (any, error) { return i.Args, nil }}
	for _, commands := range [][]aicli.Command{
		{{Name: "missing", Run: read.Run}}, {read, read}, {{Name: "schema", Risk: aicli.ReadRisk(), Run: read.Run}},
		{{Name: "group", Children: []aicli.Command{read}, Risk: aicli.ReadRisk()}},
		{{Name: "invalid", Risk: aicli.Risk{Effect: "WRITE", Confirmation: "NONE"}, Run: read.Run}},
		{{Name: "invalid", Risk: aicli.ReadRisk(), Run: read.Run, Options: []aicli.Option{{Name: "--yes", Type: aicli.Boolean}}}},
		{{Name: "invalid", Risk: aicli.ReadRisk(), Run: read.Run, Options: []aicli.Option{{Name: "--name", Type: aicli.String, Default: 12}}}},
	} {
		_, err := aicli.New(aicli.Config{Name: "test", Commands: commands})
		requireCode(t, err, "CONFIG_INVALID")
	}
	r := runtimeFor(t, aicli.Config{Commands: []aicli.Command{read}})
	read.Options[0].Default = "mutated"
	data, err := r.Execute(context.Background(), []string{"read"}, nil, aicli.ExecuteOptions{})
	if err != nil || data.(aicli.Arguments).String("--name") != "original" {
		t.Fatal(data, err)
	}
	_, err = r.Schema("unknown")
	requireCode(t, err, "UNKNOWN_COMMAND")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Execute(ctx, []string{"read"}, nil, aicli.ExecuteOptions{})
	requireCode(t, err, "EXECUTION_CANCELLED")
}

func TestCobraCLIAndConcurrentReuse(t *testing.T) {
	r := runtimeFor(t, aicli.Config{Version: "0.1.0", Commands: []aicli.Command{{Name: "group", Children: []aicli.Command{
		{Name: "+write", Risk: aicli.WriteRisk(), Options: []aicli.Option{{Name: "--name", Type: aicli.String, Required: true}, {Name: "--enabled", Type: aicli.Boolean}, {Name: "--count", Type: aicli.Number, Default: 3}}, Run: func(i aicli.Invocation) (any, error) { return i.Args, nil }},
		{Name: "+failure", Risk: aicli.ReadRisk(), Options: []aicli.Option{{Name: "--secret", Type: aicli.String, Required: true}}, Run: func(i aicli.Invocation) (any, error) {
			return nil, &aicli.Error{Code: "BUSINESS_FAILED", Message: "failed " + i.Args.String("--secret"), Details: map[string]any{"nested": []any{i.Args.String("--secret")}}}
		}},
	}}}})
	for _, test := range []struct {
		argv     []string
		code     int
		contains string
	}{
		{[]string{"--help"}, 0, "Available Commands"}, {[]string{"--version"}, 0, "0.1.0"},
		{[]string{"schema", "group", "+write"}, 0, `"effect": "WRITE"`},
		{[]string{"group", "+write", "--name=A", "--dry-run", "--format=json"}, 0, `"dryRun": true`},
		{[]string{"group", "+write", "--name=A", "-y", "--enabled=false", "--count=-2", "--format=json"}, 0, `"--count": -2`},
		{[]string{"group", "+write", "--name=A"}, 2, "WRITE_APPROVAL_REQUIRED"},
		{[]string{"group", "+write", "--count=not-number"}, 2, "INVALID_ARGUMENT"},
		{[]string{"group", "+write", "--name=A", "--dry-run", "--format=xml"}, 2, "INVALID_ARGUMENT"},
		{[]string{"group", "+failure", "--secret", "fixture-secret"}, 2, "[REDACTED]"},
	} {
		var out, errOut bytes.Buffer
		code := r.RunCLI(context.Background(), test.argv, &out, &errOut, aicli.CLIOptions{})
		text := out.String() + errOut.String()
		if code != test.code || !strings.Contains(text, test.contains) || strings.Contains(text, "fixture-secret") {
			t.Fatalf("%v: code %d output %s", test.argv, code, text)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out, errOut bytes.Buffer
			name := fmt.Sprint(i)
			if code := r.RunCLI(context.Background(), []string{"group", "+write", "--name=" + name, "--dry-run"}, &out, &errOut, aicli.CLIOptions{}); code != 0 {
				t.Errorf("concurrent CLI failed: %s", &errOut)
			}
			var plan aicli.Plan
			if json.Unmarshal(out.Bytes(), &plan) != nil || plan.Arguments.String("--name") != name {
				t.Errorf("mixed invocation: %s", &out)
			}
		}(i)
	}
	wg.Wait()
}
