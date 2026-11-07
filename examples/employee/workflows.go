package employee

import (
	aicli "github.com/redSun64/ai-cli"
	"net/url"
)

func accountStatus(call aicli.Invocation, input StatusInput) (Account, error) {
	var account Account
	err := call.HTTP.Do(call.Context, aicli.Request{Path: "/accounts/" + url.PathEscape(input.AccountID)}, &account)
	return account, err
}
func grantRole(call aicli.Invocation, input GrantRoleInput) (map[string]string, error) {
	body := map[string]string{"accountId": input.AccountID, "role": input.Role}
	err := call.HTTP.Do(call.Context, aicli.Request{Method: "POST", Path: "/roles/bind", Body: body}, nil)
	return body, err
}
func onboard(call aicli.Invocation, input OnboardInput) (map[string]any, error) {
	var person Employee
	if err := call.HTTP.Do(call.Context, aicli.Request{Path: "/employees/search", Query: url.Values{"keyword": {input.Name}}}, &person); err != nil {
		return nil, err
	}
	var account Account
	if err := call.HTTP.Do(call.Context, aicli.Request{Method: "POST", Path: "/accounts", Body: map[string]string{"employeeId": person.ID, "name": person.Name}}, &account); err != nil {
		return nil, err
	}
	if _, err := grantRole(call, GrantRoleInput{AccountID: account.ID, Role: input.Role}); err != nil {
		return nil, &aicli.Error{Code: "EMPLOYEE_ONBOARD_PARTIAL_SUCCESS", Message: "Account was created, but role binding failed or its effect is unknown.", Details: map[string]any{"employeeId": person.ID, "accountId": account.ID, "failedStep": "bindRole", "recovery": "Query +status for this account, then preview +grant-role if binding still needs recovery. Do not onboard again."}}
	}
	return map[string]any{"employee": person, "account": account, "role": input.Role}, nil
}
