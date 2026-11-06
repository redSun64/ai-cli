// Package employee demonstrates a business application built with the SDK.
// Importing this package does not start the CLI or contact the service.
package employee

import (
	"embed"
	"io/fs"
	"net/url"
	"time"

	aicli "github.com/redSun64/ai-cli"
)

//go:embed skills/employee
var skillFiles embed.FS

// Options configures the example service. LookupEnv may be supplied by tests or
// an embedding host; when nil, the SDK reads process environment variables.
type Options struct {
	BaseURL         string
	LookupEnv       func(string) string
	CredentialStore aicli.CredentialStore
}

// Employee and Account represent responses returned by the example API.
type Employee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Account struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}

// New creates a CLI with status, role recovery and multi-step onboarding commands.
func New(options Options) (*aicli.Runtime, error) {
	base := options.BaseURL
	if base == "" {
		base = "http://localhost:18080"
	}
	auth, err := aicli.NewAuth(aicli.AuthConfig{DomainID: "employee", Type: "bearer", CredentialKey: "EMPLOYEE_API_TOKEN", Required: true, Store: options.CredentialStore, LookupEnv: options.LookupEnv})
	if err != nil {
		return nil, err
	}
	skill, err := fs.Sub(skillFiles, "skills/employee")
	if err != nil {
		return nil, err
	}
	return aicli.New(aicli.Config{
		Name: "employee", Description: "Employee business capabilities.", Version: "0.1.0", Timeout: 30 * time.Second,
		Auth:  auth,
		HTTP:  &aicli.HTTPConfig{BaseURL: base, BaseURLEnv: "EMPLOYEE_BASE_URL", LookupEnv: options.LookupEnv},
		Skill: &aicli.SkillBundle{ID: "employee", Files: skill},
		Commands: []aicli.Command{{Name: "employee", Description: "Employee account workflows.", Children: []aicli.Command{
			{Name: "+status", Description: "Query account status.", Risk: aicli.ReadRisk(),
				Options: []aicli.Option{{Name: "--account-id", Type: aicli.String, Required: true, Description: "Account ID."}},
				Agent:   &aicli.AgentMetadata{Summary: "查询已有账户状态。", UseWhen: []string{"核验账户状态或写入效果"}, AvoidWhen: []string{"创建新账户"}, Examples: []string{"查询 acc-1024 的状态"}},
				Run: func(call aicli.Invocation) (any, error) {
					var account Account
					err := call.HTTP.Do(call.Context, aicli.Request{Path: "/accounts/" + url.PathEscape(call.Args.String("--account-id"))}, &account)
					return account, err
				}},
			{Name: "+grant-role", Description: "Bind a role to an existing account.", Risk: aicli.WriteRisk(),
				Options: []aicli.Option{{Name: "--account-id", Type: aicli.String, Required: true}, {Name: "--role", Type: aicli.String, Required: true}},
				Run: func(call aicli.Invocation) (any, error) {
					err := call.HTTP.Do(call.Context, aicli.Request{Method: "POST", Path: "/roles/bind", Body: map[string]string{"accountId": call.Args.String("--account-id"), "role": call.Args.String("--role")}}, nil)
					return map[string]string{"accountId": call.Args.String("--account-id"), "role": call.Args.String("--role")}, err
				}},
			{Name: "+onboard", Description: "Provision an enterprise account and default role for an employee.", Risk: aicli.WriteRisk(),
				Options: []aicli.Option{{Name: "--name", Type: aicli.String, Required: true, Description: "Employee name."}, {Name: "--role", Type: aicli.String, Default: "employee", Description: "Default role."}},
				Agent:   &aicli.AgentMetadata{Summary: "为企业员工初始化账号和默认角色。", UseWhen: []string{"用户要求给新员工开通系统账号"}, AvoidWhen: []string{"用户只是查询员工信息", "已有账户只需补绑角色"}, Examples: []string{"给张三开通系统账号"}},
				Run: func(call aicli.Invocation) (any, error) {
					var person Employee
					if err := call.HTTP.Do(call.Context, aicli.Request{Path: "/employees/search", Query: url.Values{"keyword": {call.Args.String("--name")}}}, &person); err != nil {
						return nil, err
					}
					var account Account
					if err := call.HTTP.Do(call.Context, aicli.Request{Method: "POST", Path: "/accounts", Body: map[string]string{"employeeId": person.ID, "name": person.Name}}, &account); err != nil {
						return nil, err
					}
					if err := call.HTTP.Do(call.Context, aicli.Request{Method: "POST", Path: "/roles/bind", Body: map[string]string{"accountId": account.ID, "role": call.Args.String("--role")}}, nil); err != nil {
						return nil, &aicli.Error{Code: "EMPLOYEE_ONBOARD_PARTIAL_SUCCESS", Message: "Account was created, but role binding failed or its effect is unknown.", Details: map[string]any{"employeeId": person.ID, "accountId": account.ID, "failedStep": "bindRole", "recovery": "Query +status for this account, then preview +grant-role if binding still needs recovery. Do not onboard again."}}
					}
					return map[string]any{"employee": person, "account": account, "role": call.Args.String("--role")}, nil
				}},
		}}},
	})
}
