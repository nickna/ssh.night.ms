# Contributing

Thanks for being interested in the project. This is a small codebase
maintained as a side project, so the bar for contributions is "does it
make the BBS measurably better without making the code harder to live
with." Drive-by fixes, new screens, new doors games, and provider
integrations are all welcome.

## Before you start

- **Big changes:** open an issue first to talk through the approach.
  It's cheap to course-correct on a sketch, expensive to course-correct
  on a finished PR.
- **Security issues:** do not open a public issue. See
  [`SECURITY.md`](./SECURITY.md) for the private disclosure path.
- **Architecture:** [`CLAUDE.md`](./CLAUDE.md) is the deepest single-file
  description of how the pieces fit together — start there before
  refactoring anything cross-cutting.

## Dev loop

```sh
pwsh ./run.ps1        # boot Postgres + Redis in Docker, build, run
pwsh ./run.ps1 -Stop  # tear down containers
go test ./...         # full test suite (no special harness)
go vet ./...
```

After editing `internal/data/queries/*.sql` or migrations, regenerate:

```sh
sqlc generate
```

## Code style

- Standard Go formatting: `gofmt`, `go vet`. No additional linters required.
- Prefer the existing patterns over inventing new ones. The composition
  root is `cmd/nightms/main.go` — start by reading it end-to-end.
- New TUI screens go under `internal/tui/screens/`; route them via
  `nav.Dest*` constants and a case in `internal/tui/app.go::route()`.
- New providers (outbound HTTP integrations) reuse the shared transport
  built in `buildProviders` — don't construct `http.Client{}` with the
  default transport.
- Comments are reserved for non-obvious "why." Don't restate "what" the
  code does in a comment; let the names carry it.

## Tests

- Unit tests live next to the code (`foo.go` → `foo_test.go`). No
  special harness; plain `go test`.
- Integration tests against a real Postgres / Redis run through the same
  `go test` invocation. `run.ps1` brings the dependencies up; once
  they're running, `go test ./...` works locally without further setup.
- New features that touch realtime fan-out, auth, or persistence should
  carry tests. Pure-rendering TUI changes usually don't need them.

## Pull requests

- One logical change per PR. If you find an unrelated bug along the way,
  open a separate PR — it gets merged faster.
- Reference the relevant issue or motivation in the PR description.
- Don't worry about squashing — maintainers handle the final merge
  strategy.

## What's out of scope

- Rewrites of established subsystems (auth, transport, the bubbletea
  root model) without prior discussion.
- Cosmetic-only churn (renaming for taste, reformatting whole files).
- New features that need a new always-on background service or a new
  external dependency, unless there's clear value justifying the
  operational cost.

## License

By submitting a contribution you agree it can be released under the
project's MIT license. No CLA, no separate paperwork.
