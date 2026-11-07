# 类型化命令（当前源码，尚未发布）

业务只声明输入、场景、风险和处理函数。SDK 从同一声明生成参数、帮助、Schema、默认值和执行绑定，不需要再写一份参数表或字符串分发器。

```go
type SearchInput struct {
    Keyword string `cli:"keyword,required" help:"Search by name."`
    Limit   int    `cli:"limit" default:"20" help:"Maximum results."`
}

func search(call aicli.Invocation, input SearchInput) ([]Person, error) {
    // 调用业务服务；通过 call.HTTP 继承认证、超时和当前命令权限。
    return findPeople(call, input.Keyword, input.Limit)
}

commands := []aicli.Command{
    aicli.Group("people", "People operations",
        aicli.Typed(aicli.Command{
            Name: "+search", Description: "Search people", Risk: aicli.ReadRisk(),
            Agent: &aicli.AgentMetadata{
                Summary: "Resolve people before assigning work.",
                Examples: []string{"people +search --keyword Zhang"},
            },
        }, search),
    ),
}
// 传入 aicli.Config.Commands，再调用 aicli.New。
```

`Person` 和 `findPeople` 代表业务自己的类型和服务。完整可运行版本见 [员工示例](../examples/employee/commands.go) 及其 [业务流程](../examples/employee/workflows.go)。

## 输入约定

- 输入是 struct，字段使用 `cli:"name"`，可附加 `required`、`sensitive`。`help` 是参数说明，`default` 是按字段类型解析的字符串默认值。
- 支持字符串、布尔值、有符号／无符号整数、浮点数、其别名及一层指针。不支持切片、map、任意嵌套对象。业务 ID 用字符串。
- `*int`、`*bool`、`*string` 区分未提供与显式 `0`、`false`、空串；有默认值的指针会在缺省时填充默认值。
- 可嵌入导出的非指针 struct 共享参数组。内部字段使用 `cli:"-"`；导出字段漏写标签会被拒绝。
- 整数仍沿用数字参数的 `Double` Schema 类型，额外输出 `integer:true`。SDK 在预演和确认前拒绝小数、溢出及超出 JSON 安全整数范围的值。具体业务上下限仍由业务校验。
- 声明错误由 `aicli.New` 返回 `CONFIG_INVALID`，不 panic。`Typed` 不接受手工 `Options`、`Run` 或子命令，生成后也不要覆盖这些字段。

原有使用具名字段的 `Command{Options: ..., Run: ...}` 保持可用，适合需要直接处理 `Arguments` 的适配层。`Group` 和 `Typed` 不引入另一套 Runtime，也不改变写入确认、只读 HTTP 限制、认证和 Skill 安装协议。

## 建议结构

```text
app.go          组合配置、认证、HTTP、Skill、业务模块
commands_*.go   按业务模块声明命令并直接绑定处理函数
inputs.go       输入与共享参数组
workflows.go    业务校验、服务调用、多步流程和恢复
```

小业务可以合并文件；SDK 不要求目录布局。业务无需维护全局 switch、反射注册表或帮助文档副本。SDK 不接管业务响应封装、推荐评分、审批状态或分页完整性判断。

设计参考 [钉钉 CLI](https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli) 的运行时命令树和统一契约投影思路；这里只保留单一命令声明与模块组合，不引入其产品目录及兼容层。

## 发布前本地联调

在包含业务仓库与本地 SDK 的父目录执行：

```sh
go work init ./business-cli ./ai-cli
go test ./business-cli/...
```

已有 workspace 则使用 `go work use` 加入两者；路径按实际检出位置调整。不要把机器绝对路径或临时 `replace` 提交到业务模块。发布 SDK 新版本后，业务再升级 `go.mod` 并用 `GOWORK=off` 验证独立构建。
