# Contributing to ProbeGlass

Thanks for taking the time to contribute. ProbeGlass is a lightweight,
self-hosted monitoring daemon for homelabs, and it stays small on purpose. Every
contribution — a bug report, a docs fix, a new probe engine — helps.

> **Set your expectations here, not at the issue tracker.** ProbeGlass is a hobby
> project: one person who is not a professional software developer, spare time, and a
> codebase written in dialogue with an AI coding agent. It was built for their own
> homelab and shared in case somebody else is looking for something similar. Reports
> and patches are read and appreciated, but there is no support contract, no response
> time, and no promise that anything gets fixed. If that is the trade you want, you
> are very welcome — [Who makes this, and
> how](https://j000k3r.github.io/probeglass-docs/about) says the rest.

## Welcome / Ways to contribute

- Report bugs and rough edges via the issue tracker.
- Suggest features or improvements you would find useful.
- Improve documentation or fix typos.
- Submit code: new probe engines, alert channels, dashboard work, tests.

If you are unsure where to start, open a discussion or comment on an existing
issue before writing code.

## Reporting bugs

Open an issue using the **Bug report** form. Please include:

- A clear, descriptive title.
- Steps to reproduce the problem.
- What you expected to happen and what actually happened.
- Your ProbeGlass version.
- How you deployed it (Docker Compose, `docker run`, Kubernetes, binary, other).
- Relevant logs with secrets redacted.

Search existing issues first — the problem may already be reported.

## Suggesting features

Open an issue using the **Feature request** form. Describe the problem or
motivation first, then the proposed solution, then any alternatives you
considered. Features that fit ProbeGlass's scope — small, self-hosted,
single-container monitoring — are the most likely to be accepted.

## Development setup

Requirements:

- Go 1.27 or newer.
- Docker (for building and testing the container image).

Common commands:

- `make run` — build and serve ProbeGlass on `:8080`, storing data in
  `./data`.
- `make test` — run the test suite.
- `make test-browser` — render the dashboard in a real browser. Needs Chrome or
  Chromium and skips without one; see below.
- `make lint` — run the linters.
- `make build` — build the `probeglass` binary.

There is no frontend build step: the dashboard is vanilla JS/CSS embedded into
the binary.

### The browser test

`e2e/` is a separate Go module holding one test that drives a headless browser
through first-run setup and asserts on what is on screen. It exists because the
rest of the suite only ever talks to the API, and the dashboard once shipped a
bug where every endpoint returned `200` while a full-height spinner covered the
page — a CSS rule outranking the `hidden` attribute, which nothing that does not
render a page can see.

Two things follow from it being in its own module. `go test ./...` in the
repository root does not descend into it, so CI runs it by name; and the browser
driver stays out of the product's dependency list, which is the point.

## Project layout

- `cmd/probeglass` — the program entrypoint.
- `internal/config` — configuration loading and validation.
- `internal/store` — SQLite persistence and history rollups.
- `internal/sensor` — probe engines (HTTP, TCP, ping, DNS, …).
- `internal/scheduler` — schedules and runs sensors.
- `internal/notify` — alert channels.
- `internal/api` — REST API and SSE streams.
- `internal/web` — embedded dashboard assets.

## Coding guidelines

- Write idiomatic Go and run `gofmt` on everything you touch.
- Keep dependencies minimal and justify any new one in the pull request.
- Do not add runtime dependencies to the frontend. The dashboard must remain
  vanilla JS/CSS with no npm or build step.
- Prefer table-driven tests, and add a test for every behaviour change.
- Write commit messages in the conventional-commit style, for example
  `fix(scheduler): avoid drift on long intervals`.

## Commit and pull request process

1. Branch from `main` (for example `feat/dns-sensor-timeout`).
2. Keep each pull request focused on a single change.
3. Add tests covering the new behaviour, or explain in the PR why tests do not
   apply.
4. Update `CHANGELOG.md` under the `Unreleased` heading.
5. Fill in the pull request template and confirm that `make lint` and
   `make test` pass locally. If you touched the dashboard's markup, styles or
   JavaScript, run `make test-browser` as well.

Maintainers aim to review pull requests within a few days. Small, well-scoped
PRs are reviewed fastest.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
