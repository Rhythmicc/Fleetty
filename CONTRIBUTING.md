English | [简体中文](CONTRIBUTING.zh-CN.md)

# Contributing

Issues and pull requests are welcome.

## Build and test

Use the Go version declared in [go.mod](go.mod).

```bash
go test -race ./...
go vet ./...
gofmt -l $(git ls-files '*.go')
go mod tidy
git diff --exit-code -- go.mod go.sum
```

`gofmt -l` should print nothing. CI runs the same checks, plus `govulncheck`, on Linux, and the race tests and `go vet` again on macOS. The workflow is [.github/workflows/ci.yml](.github/workflows/ci.yml).

## Pull requests

- Keep a change focused on one problem.
- Match the code around it. When you replace something, delete the old path. Do not leave a compatibility shim, a migration, or a fallback for it.
- Prefer the smallest change that works end to end. Do not add a configuration layer for a case that does not exist yet.
- When a flag, a config key, or a visible behavior changes, update [the English docs](docs/en/) and [the Chinese docs](docs/zh-CN/) in the same pull request. Commands, flags, config keys, and fenced code blocks must be identical in both languages.
- Do not commit secrets, host keys, or password hashes.

## License

By contributing, you agree that your contribution is licensed under the [MIT License](LICENSE).
