[English](CONTRIBUTING.md) | 简体中文

# 参与贡献

欢迎报告问题，也欢迎拉取请求。

## 构建与测试

Go 版本以 [go.mod](go.mod) 为准。

```bash
go test -race ./...
go vet ./...
gofmt -l $(git ls-files '*.go')
go mod tidy
git diff --exit-code -- go.mod go.sum
```

`gofmt -l` 不应有输出。CI 在 Linux 上运行同样的检查，并加上 `govulncheck`；macOS 上再跑一遍竞态测试和 `go vet`。工作流是 [.github/workflows/ci.yml](.github/workflows/ci.yml)。

## 拉取请求

- 一次改动只解决一个问题。
- 写法跟周围代码一致。替换旧路径时直接删掉它，不要留兼容层、迁移或回退。
- 用能端到端跑通的最小改动。还没有的场景，不要先加一层配置。
- 标志、配置项或可见行为有变化时，在同一个拉取请求里同时更新[英文文档](docs/en/)和[中文文档](docs/zh-CN/)。命令、标志、配置键和围栏代码块在两种语言里必须一致。
- 不要提交密钥、host key 或密码哈希。

## 许可证

提交贡献即表示你同意以 [MIT 许可证](LICENSE)授权该贡献。
