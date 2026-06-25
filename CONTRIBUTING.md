# Contributing

Thanks for your interest in improving the Traefik Maintenance Plugin.

## Hard constraints

This plugin is loaded by Traefik through the [Yaegi](https://github.com/traefik/yaegi)
interpreter, which imposes two rules that override normal Go style:

- **Standard library only — zero external dependencies.** `go.mod` has no `require`
  block and there is no `go.sum`. Do not add third-party modules; if you reach for a
  dependency, solve it with the standard library instead.
- **Stay Yaegi-compatible.** Yaegi does not implement every Go feature. Avoid
  constructs it cannot interpret — in particular the `slices.*` / `maps.*` generic
  helpers and range-over-int loops. When in doubt, run the Yaegi test (below); if it
  fails, rewrite using plain loops and explicit code.

Keep the module import path, the `.traefik.yml` plugin id, and the maintenance-API
JSON field names unchanged — Traefik and existing deployments depend on them.

## Development

Requires Go 1.25 or newer (`go.mod` pins the toolchain).

```bash
make test        # go test -race -cover ./...
make lint        # golangci-lint run
make yaegi_test  # interpret the plugin under Yaegi (requires yaegi on PATH)
gofmt -l .       # must print nothing
go vet ./...
```

Install Yaegi for the interpreter check:

```bash
go install github.com/traefik/yaegi/cmd/yaegi@latest
```

## What CI checks

Every pull request must pass:

- `gofmt` formatting and `go vet`
- `go test -race` with coverage (reported to Codecov)
- `golangci-lint`
- `govulncheck` — no calls into known-vulnerable standard-library code
- `yaegi test` — proof the plugin still loads under the interpreter
- `go.mod` tidiness

## Pull requests

- Keep changes focused, and add tests for any new behavior.
- Describe the user-visible effect and document any new configuration in the README.
- Report security issues privately — see [SECURITY.md](SECURITY.md), not a public issue.
