# AI CLI SDK for Go

业务方用 Go 定义命令和业务流程，SDK 统一提供 Cobra 命令行、渐进式 `schema`、参数校验、预演、写入确认、认证、HTTP 调用和 Agent Skill 安装。同一套命令可通过终端或进程内 API 调用。

Go 1.23+。Module：`github.com/redSun64/ai-cli`，包名：`aicli`。CLI 使用 [Cobra](https://github.com/spf13/cobra)，无需自行实现参数解析和帮助系统。

## 业务方快速接入

在自己的业务工程执行：

```sh
go mod init example.com/hr-cli
go get github.com/redSun64/ai-cli@v0.1.0
```

创建 `main.go`：

```go
package main

import (
    "context"
    "os"

    aicli "github.com/redSun64/ai-cli"
)

func main() {
    auth, err := aicli.NewAuth(aicli.AuthConfig{
        DomainID: "hr", Type: "bearer",
        CredentialKey: "HR_API_TOKEN", Required: true,
    })
    if err != nil { panic(err) }

    app, err := aicli.New(aicli.Config{
        Name: "hr", Description: "HR business CLI", Auth: auth,
        HTTP: &aicli.HTTPConfig{
            BaseURL: "http://localhost:18080", BaseURLEnv: "HR_BASE_URL",
        },
        Commands: []aicli.Command{{
            Name: "account", Children: []aicli.Command{{
                Name: "+status", Description: "Query account status",
                Risk: aicli.ReadRisk(),
                Options: []aicli.Option{{Name: "--account-id", Type: aicli.String, Required: true}},
                Run: func(call aicli.Invocation) (any, error) {
                    var result map[string]any
                    err := call.HTTP.Do(call.Context, aicli.Request{
                        Path: "/accounts/status",
                        Query: map[string][]string{"accountId": {call.Args.String("--account-id")}},
                    }, &result)
                    return result, err
                },
            }},
        }},
    })
    if err != nil { panic(err) }
    os.Exit(app.RunCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr, aicli.CLIOptions{}))
}
```

```sh
go build -o hr .
./hr --help
./hr schema
./hr schema account +status
./hr account +status --account-id acc-1024 --dry-run --format json
```

上述发现和预演不需要服务或凭据。实际请求前，由人类或部署系统配置 `HR_BASE_URL` 和 `HR_API_TOKEN`；业务代码不用读取 Token 或拼装认证头。Windows 构建为 `hr.exe`，运行时使用 `.\hr.exe`。

写命令设置 `Risk: aicli.WriteRisk()`；HTTP 请求通过 `Method: "POST"` 等声明实际方法。执行流程是 `--dry-run` 预演 → 用户明确授权 → `--yes` 执行。SDK 检查确认标记，授权来源由接入方负责。

## 完整员工示例

```sh
go install github.com/redSun64/ai-cli/examples/employee/cmd/employee@v0.1.0
employee schema employee +onboard
employee employee +onboard --name Example --dry-run --format json
```

启动器和业务组都名为 `employee`，因此上面的两层 `employee` 分别是可执行文件和命令组。安装后的可执行文件在 `go env GOBIN` 或 `$(go env GOPATH)/bin`，将该目录加入 PATH，或使用绝对路径运行。

从源码运行：

```sh
go run ./examples/employee/cmd/employee schema employee +onboard
go build -trimpath -o dist/employee ./examples/employee/cmd/employee
```

示例包括查员工 → 建账户 → 绑角色的业务编排，以及账户查询、角色补救和内嵌 Skill。服务地址覆盖变量为 `EMPLOYEE_BASE_URL`，认证变量为 `EMPLOYEE_API_TOKEN`；实际执行默认要求认证。

## 程序内调用

```go
schema, err := app.Schema("account", "+status")
result, err := app.Execute(ctx, []string{"account", "+status"},
    aicli.Arguments{"--account-id": "acc-1024"},
    aicli.ExecuteOptions{DryRun: true})
```

真正执行已授权写操作使用 `ExecuteOptions{Confirmed: true}`。`Execute` 返回数据和 `*aicli.Error`；CLI 的 `--format json` 返回 `{ "ok": true, "data": ... }`，失败向 stderr 输出 `{ "ok": false, "error": ... }`。`schema` 始终输出原始 schema。

已有 Cobra 应用可以 `root.AddCommand(app.CobraCommand(aicli.CLIOptions{}))`；每次调用生成新的命令树。参数默认值、风险和业务处理函数以 SDK 定义为准。

## 交付与更多说明

`go build` 生成包含 SDK 和 Skill 的本机可执行文件，用户无需 Go 运行时。[接入参考](docs/usage.md)说明认证扩展、HTTP 语义、Skill、并发和错误恢复；[版本与发布](docs/releasing.md)说明 Go module 标签规则。

本版本没有默认系统钥匙串、终端隐藏输入、YAML 配置加载或 PATH 自动修改。环境变量认证可直接使用；持久凭据和交互登录通过扩展接口接入。
