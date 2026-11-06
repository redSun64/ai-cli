package employee_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	aicli "github.com/redSun64/ai-cli"
	"github.com/redSun64/ai-cli/examples/employee"
)

func TestOnboardingAndRecovery(t *testing.T) {
	for _, failRole := range []bool{false, true} {
		t.Run(fmt.Sprint(failRole), func(t *testing.T) {
			steps := []string{}
			var mu sync.Mutex
			getSteps := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, steps...) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				steps = append(steps, r.Method+" "+r.URL.Path)
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer fixture-credential" {
					t.Error("missing authentication")
				}
				switch r.URL.Path {
				case "/employees/search":
					if r.URL.Query().Get("keyword") != "Example" {
						t.Error("missing search parameter")
					}
					fmt.Fprint(w, `{"id":"emp-1","name":"Example"}`)
				case "/accounts":
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["employeeId"] != "emp-1" {
						t.Error("incorrect account input")
					}
					fmt.Fprint(w, `{"id":"acc-1"}`)
				case "/roles/bind":
					if failRole {
						w.WriteHeader(503)
					} else {
						w.WriteHeader(204)
					}
				case "/accounts/acc-1":
					fmt.Fprint(w, `{"id":"acc-1","status":"active"}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			app, err := employee.New(employee.Options{BaseURL: server.URL, LookupEnv: func(key string) string {
				if key == "EMPLOYEE_API_TOKEN" {
					return "fixture-credential"
				}
				return ""
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = app.Execute(context.Background(), []string{"employee", "+onboard"}, aicli.Arguments{"--name": "Example"}, aicli.ExecuteOptions{DryRun: true})
			if err != nil || len(getSteps()) != 0 {
				t.Fatal("preview invoked business", err, getSteps())
			}
			_, err = app.Execute(context.Background(), []string{"employee", "+onboard"}, aicli.Arguments{"--name": "Example"}, aicli.ExecuteOptions{Confirmed: true})
			if failRole {
				var e *aicli.Error
				if !errors.As(err, &e) || e.Code != "EMPLOYEE_ONBOARD_PARTIAL_SUCCESS" || e.Details["accountId"] != "acc-1" {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := []string{"GET /employees/search", "POST /accounts", "POST /roles/bind"}
			if !reflect.DeepEqual(getSteps(), want) {
				t.Fatal(getSteps())
			}
			_, err = app.Execute(context.Background(), []string{"employee", "+status"}, aicli.Arguments{"--account-id": "acc-1"}, aicli.ExecuteOptions{})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
