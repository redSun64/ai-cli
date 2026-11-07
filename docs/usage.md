# 接入参考

## 命令定义

当前源码推荐通过 [Group / Typed](typed-commands.md) 声明业务模块与类型化输入；下面的低层 `Command` 接口仍受支持。

父 `Command` 提供 `Name`、`Description`、非 nil 的 `Children`，叶子提供 `Options`、`Risk` 和 `Run`。叶子不能同时承担命令组。根级保留 `schema`、`auth`、`skills`、`help`。

参数名使用 `--account-id` 这样的 CLI 拼写。支持 `String`、`Boolean`、`Number`；默认值必须类型一致，数字统一归一化为 `float64`，拒绝 NaN/Inf。CLI 的解析和帮助由 Cobra/pflag 提供；`--name=value`、布尔值 `--enabled=false`、`-y` 等沿用 Cobra 语法。进程内输入同样使用 CLI 参数名。浮点参数不适合需要精确表达的大整数，业务 ID 应使用字符串。

用 `AgentMetadata` 描述 `Summary`、`UseWhen`、`AvoidWhen`、`Examples`，schema 会携带这些场景说明。构建 Runtime 时快照命令元数据；后续修改原始配置不改变 Runtime。

`ReadRisk()` 是 READ/NONE；`WriteRisk()` 是 WRITE/REQUIRED，其他组合在构建时被拒绝。预演完成参数校验和默认值填充后直接返回，不执行 Handler、认证提供者或请求。

`Option.Sensitive` 或名称包含 password/token/secret/credential/api-key 等模式的参数，在预演和 SDK 错误中脱敏，schema 和帮助不展示敏感默认值。业务返回值和业务自行打印的日志由接入方负责脱敏。

## 认证与服务地址

`HTTPConfig.BaseURL` 配置单个 HTTP(S) 源，`BaseURLEnv` 指定环境覆盖。使用 Go struct 配置，不包含 YAML 解析器。相对路径使用标准 URL ResolveReference 语义：`/accounts` 从源根目录开始，`accounts` 相对 base path，base path 末尾斜杠影响结果。

`NewAuth(AuthConfig)` 提供 `none`、`bearer`、`api-key`；环境中非空的 `CredentialKey` 对应值优先，其次查询 `CredentialStore`。API Key 用 `Header` 指定服务端头名，默认 Authorization；`Prefix` 可覆盖前缀。自定义签名或换票逻辑实现 `AuthProvider.Headers(ctx, method, url)`，通过 `HTTPConfig.Auth` 注入。

`CredentialStore` 的 Get/Set/Delete 接收 domain 和 key。`MemoryStore` 只适合测试和进程内临时使用，退出即丢失；持久化请实现系统钥匙串或企业密钥服务适配。SDK 没有明文文件存储。认证提供者、存储和业务 Handler 必须支持并发并遵守 context。

`auth status` 只证明本地凭据存在，不证明服务端有效性；缺失时 CLI 退出码 3。`auth logout --yes` 只删除对应域的存储凭据，环境凭据仍然生效；支持 `--dry-run`。程序内 `Login`/`Logout` 由业务调用方负责授权。

交互登录需同时配置持久 `CredentialStore` 和 `CLIOptions.ReadSecret`，该回调必须隐藏输入、响应取消。未配置回调时 `auth login` 返回 `AUTH_INTERACTIVE_REQUIRED`，不会从命令行参数或普通 stdin 读取密钥。员工示例默认使用环境变量认证。

## HTTP 与取消

`Invocation.HTTP.Do` 统一认证、JSON 编解码和失败行为；`out` 为 nil 时丢弃响应，`*string` / `*[]byte` 获取原文，其他指针按 JSON 解码。204/空响应不解码。

- HTTP 默认超时 15 秒，Runtime 默认执行超时 30 秒；使用较早的取消信号。Handler 和自定义 provider 的非 HTTP 工作应主动检查 `Invocation.Context`，SDK 不会强制终止 Go goroutine。
- HTTP 响应上限默认 8 MiB，可通过 `MaxResponseBytes` 修改，最大 1 GiB。
- URL 必须同源，禁止 userinfo、fragment 和自动重定向。错误不输出请求 URL、凭据或远端响应体。
- READ 命令仅允许 GET/HEAD/OPTIONS。确定无写入副作用的 POST 查询由业务负责人逐条声明 `ReadOnlyPostPaths`，使用包含 base path 的完整规范 pathname，不使用通配符。URL 中有转义 pathname 的 POST 不匹配该白名单。
- 认证通过 `AuthProvider` 注入；请求不能直接设置 Authorization/Host/Cookie，也不能覆盖 provider 已生成的认证头。使用 API Key 的业务应始终配置 provider。
- WRITE 请求不由 SDK 自动重试，标准 HTTP transport 的请求回放也被禁用。`IdempotencyKey` 透传 Idempotency-Key；服务端必须实现真正的幂等语义。自定义 `Transport` 自身不得重试写入。
- Client 的权限绑定当前执行实例，Handler 返回后失效。Handler 应等待自己的子任务完成再返回，避免后台工作与执行生命周期脱离。

进程内 `Execute` 支持并发，READ 和 WRITE 不共享可变风险状态；每次 CLI 执行也生成独立 Cobra 树。调用方负责自定义依赖的线程安全。业务代码仍可创建自己的 HTTP 客户端；SDK 的保证覆盖通过 SDK 执行的请求，并非进程沙箱。

## 错误与恢复

业务可返回 `&aicli.Error{Code: ..., Message: ..., Details: ...}` 保留业务错误码和恢复信息。普通 error 转为 COMMAND_EXECUTION_FAILED，已解析敏感参数会被遮盖。默认成功结果不自动脱敏。

| 错误 | 处理方式 |
| --- | --- |
| INVALID_ARGUMENT / UNKNOWN_COMMAND | 查询目标 schema 修正输入 |
| WRITE_APPROVAL_REQUIRED | 展示预演，取得具体操作授权后再确认执行 |
| AUTH_LOGIN_REQUIRED | 人类或部署系统配置认证 |
| WRITE_NOT_ALLOWED | 修正命令风险或确认 POST 查询白名单；不要绕过策略 |
| HTTP_IO_ERROR / HTTP_RESPONSE_TOO_LARGE / RESPONSE_DESERIALIZATION_FAILED | 写入效果可能未知；先查询业务状态 |
| HTTP_ERROR | 根据状态码和业务语义核验；SDK 不打印远端错误体 |
| EMPLOYEE_ONBOARD_PARTIAL_SUCCESS | 保留 accountId，核验状态，按需补绑角色；不要重新开户 |

多步骤流程没有分布式事务或自动回滚。账户创建成功、角色绑定失败时，示例保留账户并返回 accountId、failedStep、recovery。超时不能推断远端未执行。

## Agent Skill

业务工程使用 `go:embed` 和 `fs.Sub` 提供单个 Skill 根目录，再设置 `Config.Skill = &aicli.SkillBundle{ID: "employee", Files: files}`。参考员工示例。

```sh
employee skills install --dry-run
employee skills install
employee skills install --dir .agents/skills
```

默认安装到 `~/.agents/skills/<ID>/`；`--dir` 指定父目录。启动器应先构建到稳定位置，再安装 Skill；不要从 `go run` 的临时可执行文件安装。SDK 在前言后注入执行协议，替换 Markdown 中的 `{{ACLI_COMMAND}}` 为带引号的启动器绝对路径；Windows 使用 PowerShell 调用语法。Skill 安装不会修改 PATH。

`SKILL.md` 需要 plain `name: <ID>` 和单行 `description:`；这里只校验所支持的前言子集，并非完整 YAML/Agent Skills 规范验证器。允许 `references/`、`scripts/`、`assets/`；单文件上限 4 MiB，最多 256 个文件，总计 16 MiB。支持预演和显式 `--force` 覆盖本 bundle 文件，保留无关文件；每个文件原子替换，整个 bundle 不保证事务性，失败后按输出检查部分安装。

安装目录应归当前用户所有，安装期间不允许其他进程修改；SDK 拒绝已知符号链接路径和非普通文件目标，这不替代对并发恶意文件系统修改的隔离。

## 与 Java / TS 的关系

沿用 `READ/NONE`、`WRITE/REQUIRED`、`schema`、预演和 CLI JSON envelope 的核心约定。Go 采用 struct 注册和 context 执行，CLI 采用 Cobra，原生交付采用 `go build`。并非 Java 反射注解或 TS 工厂的逐 API 移植，也不包含系统钥匙串、PATH 自动管理及 YAML 配置功能。
