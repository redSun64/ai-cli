# Go module 发布

独立仓库 `https://github.com/redSun64/ai-cli` 的根目录包含 `go.mod`，module 路径为 `github.com/redSun64/ai-cli`。正式标签使用 `v0.1.0` 这样的根模块标签，不带 sdk/go 前缀；当前 Java 工作区的 sdk/go 只是开发时的源码位置。

发布前执行：

```sh
go mod tidy
go test -timeout 60s ./...
go vet ./...
go test -race -timeout 60s ./...
go build ./examples/employee/cmd/employee
```

race 检查需要支持的 C 工具链。仓库 CI 在 Linux 执行 race，在 Linux/Windows/macOS 执行普通测试和构建。跨平台编译不能替代目标平台测试。

验证通过后提交到 main，再创建并推送不可变版本标签。外部工程通过以下命令引用，不需要 replace 或本地源码：

```sh
go get github.com/redSun64/ai-cli@v0.1.0
go install github.com/redSun64/ai-cli/examples/employee/cmd/employee@v0.1.0
```

SDK 为独立根 module；维护时将 sdk/go 内容作为独立仓库根目录发布，避免推送 Java 工程、TS 工程、编译缓存或业务凭据。更新版本后同步示例 CLI Version、README 固定版本和文档安装命令。
