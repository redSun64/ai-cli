package employee

import aicli "github.com/redSun64/ai-cli"

type StatusInput struct {
	AccountID string `cli:"account-id,required" help:"Account ID."`
}
type GrantRoleInput struct {
	AccountID string `cli:"account-id,required"`
	Role      string `cli:"role,required"`
}
type OnboardInput struct {
	Name string `cli:"name,required" help:"Employee name."`
	Role string `cli:"role" default:"employee" help:"Default role."`
}

func commands() []aicli.Command {
	return []aicli.Command{aicli.Group("employee", "Employee account workflows.",
		aicli.Typed(aicli.Command{
			Name: "+status", Description: "Query account status.", Risk: aicli.ReadRisk(),
			Agent: &aicli.AgentMetadata{Summary: "查询已有账户状态。", UseWhen: []string{"核验账户状态或写入效果"}, AvoidWhen: []string{"创建新账户"}, Examples: []string{"查询 acc-1024 的状态"}},
		}, accountStatus),
		aicli.Typed(aicli.Command{
			Name: "+grant-role", Description: "Bind a role to an existing account.", Risk: aicli.WriteRisk(),
		}, grantRole),
		aicli.Typed(aicli.Command{
			Name: "+onboard", Description: "Provision an enterprise account and default role for an employee.", Risk: aicli.WriteRisk(),
			Agent: &aicli.AgentMetadata{Summary: "为企业员工初始化账号和默认角色。", UseWhen: []string{"用户要求给新员工开通系统账号"}, AvoidWhen: []string{"用户只是查询员工信息", "已有账户只需补绑角色"}, Examples: []string{"给张三开通系统账号"}},
		}, onboard),
	)}
}
