package aicli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type SharedWindow struct {
	Page int  `cli:"page" default:"1" help:"Requested page."`
	All  bool `cli:"all" default:"false" help:"Read all pages."`
}
type typedInput struct {
	SharedWindow
	ID       string  `cli:"id,required" help:"Stable ID."`
	Count    *int    `cli:"count" help:"Optional integral count."`
	Enabled  *bool   `cli:"enabled"`
	Note     *string `cli:"note"`
	Score    float64 `cli:"score" default:"0.5"`
	Internal string  `cli:"-"`
}

func TestTypedSharesSchemaCLIAndExecution(t *testing.T) {
	calls := 0
	command := Typed(Command{Name: "+read", Description: "Typed query", Risk: ReadRisk(), Agent: &AgentMetadata{Summary: "Select the read command."}}, func(call Invocation, input typedInput) (typedInput, error) {
		calls++
		if call.Context == nil || call.HTTP == nil {
			t.Fatal("missing invocation")
		}
		return input, nil
	})
	app, err := New(Config{Name: "demo", Commands: []Command{Group("records", "Record operations", command)}})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := app.Schema("records", "+read")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Agent.Summary != "Select the read command." || schema.Risk.Effect != "READ" || len(schema.Options) != 11 {
		t.Fatal(schema)
	}
	for _, option := range schema.Options {
		wantInteger := option.Name == "--page" || option.Name == "--count"
		if option.Integer != wantInteger {
			t.Fatalf("integer schema for %s: %v", option.Name, option.Integer)
		}
	}
	var out, errOut bytes.Buffer
	code := app.RunCLI(context.Background(), []string{"records", "+read", "--id", "u-1", "--count", "0", "--enabled=false", "--note=", "--format", "json"}, &out, &errOut, CLIOptions{})
	if code != 0 || calls != 1 {
		t.Fatal(code, errOut.String())
	}
	result, err := app.Execute(context.Background(), []string{"records", "+read"}, Arguments{"--id": "u-2"}, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	in := result.(typedInput)
	if in.Page != 1 || in.All || in.Count != nil || in.Enabled != nil || in.Note != nil || in.Score != 0.5 {
		t.Fatal(in)
	}
	var envelope struct{ Data typedInput }
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.Data
	if got.Count == nil || *got.Count != 0 || got.Enabled == nil || *got.Enabled || got.Note == nil || *got.Note != "" {
		t.Fatal(got)
	}
}
func TestTypedValidatesBeforeDryRunAndApproval(t *testing.T) {
	calls := 0
	cmd := Typed(Command{Name: "+write", Risk: WriteRisk()}, func(_ Invocation, in typedInput) (int, error) { calls++; return 1, nil })
	app, err := New(Config{Name: "demo", Commands: []Command{cmd}})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{1.5, 1e20, 9007199254740992.0} {
		_, err = app.Execute(context.Background(), []string{"+write"}, Arguments{"--id": "u-1", "--count": value}, ExecuteOptions{DryRun: true})
		if err == nil {
			t.Fatal("lossy integer accepted", value)
		}
	}
	_, err = app.Execute(context.Background(), []string{"+write"}, Arguments{"--id": "u-1"}, ExecuteOptions{})
	if e, ok := err.(*Error); !ok || e.Code != "WRITE_APPROVAL_REQUIRED" {
		t.Fatal(err)
	}
	_, err = app.Execute(context.Background(), []string{"+write"}, Arguments{"--id": "u-1"}, ExecuteOptions{DryRun: true})
	if err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
}
func TestTypedInvalidDeclarationsFailAtConstruction(t *testing.T) {
	cases := []Command{
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, string) (int, error) { return 0, nil }),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A []string `cli:"a"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A int `cli:"a" default:"1.5"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A int8 `cli:"a" default:"128"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A string `cli:"id"`
			B string `cli:"id"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct{ A string }) (int, error) { return 0, nil }),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A string `cli:"yes"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, struct {
			A string `cli:"a,requried"`
		}) (int, error) {
			return 0, nil
		}),
		Typed(Command{Name: "bad", Risk: ReadRisk(), Options: []Option{{Name: "--x", Type: String}}}, func(Invocation, struct{}) (int, error) { return 0, nil }),
		Typed(Command{Name: "bad", Risk: ReadRisk()}, (func(Invocation, struct{}) (int, error))(nil)),
	}
	for i, command := range cases {
		if _, err := New(Config{Name: "demo", Commands: []Command{command}}); err == nil {
			t.Fatalf("invalid declaration %d accepted", i)
		}
	}
	changed := Typed(Command{Name: "bad", Risk: ReadRisk()}, func(Invocation, typedInput) (int, error) { return 0, nil })
	changed.Options[0].Name = "--renamed"
	if _, err := New(Config{Name: "demo", Commands: []Command{changed}}); err == nil {
		t.Fatal("derived options drifted")
	}
}
func TestTypedInputsAndDefaultsAreIsolated(t *testing.T) {
	type input struct {
		Value *int `cli:"value" default:"3"`
	}
	command := Typed(Command{Name: "read", Risk: ReadRisk()}, func(_ Invocation, v input) (int, error) { original := *v.Value; *v.Value = 99; return original, nil })
	app, err := New(Config{Name: "demo", Commands: []Command{command}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := app.Execute(context.Background(), []string{"read"}, nil, ExecuteOptions{})
			if e != nil || v != 3 {
				t.Error(v, e)
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(command.Options[0].Default, float64(3)) {
		t.Fatal("default mutated")
	}
}
func TestTypedSensitivePreviewAndErrorsDoNotLeakInput(t *testing.T) {
	type input struct {
		Value string `cli:"value,sensitive"`
	}
	command := Typed(Command{Name: "read", Risk: ReadRisk()}, func(_ Invocation, v input) (any, error) { return nil, &Error{Code: "TEST", Message: v.Value} })
	app, err := New(Config{Name: "demo", Commands: []Command{command}})
	if err != nil {
		t.Fatal(err)
	}
	v, e := app.Execute(context.Background(), []string{"read"}, Arguments{"--value": "fixture-private"}, ExecuteOptions{DryRun: true})
	if e != nil || v.(Plan).Arguments["--value"] != "[REDACTED]" {
		t.Fatal(v, e)
	}
	_, e = app.Execute(context.Background(), []string{"read"}, Arguments{"--value": "fixture-private"}, ExecuteOptions{})
	if e == nil || strings.Contains(e.Error(), "fixture-private") {
		t.Fatal(e)
	}
}
