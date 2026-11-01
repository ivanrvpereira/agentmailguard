# Contributing

Thank you for your interest in agentmailguard. Bug reports, documentation fixes, and pull requests are welcome.

This project guards real mailboxes, so a change must keep the guard safe before it adds anything. Please read this page before you start.

## Before you start

- **Bugs:** open an issue with the [bug report form](https://github.com/ivanrvpereira/agentmailguard/issues/new?template=bug_report.yml). Use synthetic mail and the fake provider in reproductions. Never paste real mail, tokens, passwords, callback URLs, or files from a daemon home.
- **Security problems:** do not open an issue. Report them privately as [SECURITY.md](SECURITY.md) describes.
- **New features and larger changes:** open an issue first, so that we can agree on the behavior before you write code.
- **Ask first** before a change to: a new dependency, OAuth scopes, the questions sent to Jev, screening thresholds, policy keys or their defaults, or the set of supported operations. These change the security boundary or what users grant.

## Development setup

You need macOS or Linux, Go 1.26 or later, and a C compiler with cgo support (the Xcode Command Line Tools on macOS, or your distribution's compiler on Linux). `amgd` needs cgo for SQLite and the macOS Keychain. `amg` must build with `CGO_ENABLED=0`.

```sh
git clone https://github.com/ivanrvpereira/agentmailguard.git
cd agentmailguard
make
```

`make` checks formatting with `gofmt`, runs `go vet` and `go test -race ./...`, and builds `bin/amg` and `bin/amgd`. Run it before you open a pull request.

CI also runs `go fix -diff`, `staticcheck`, `govulncheck`, and a Gitleaks scan. To run the same checks locally:

```sh
go fix -diff ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

The end-to-end tests in `internal/e2e` build the binaries themselves, so Go can report them as `(cached)` after a code change. After a change to `cmd/`, `internal/daemon`, or `internal/cli`, run `go test -race -count=1 ./internal/e2e/`.

CI runs on Linux only, so it never builds the macOS Keychain backend (`internal/credentials/keychain_darwin.go`). After you change it, run `make` on a Mac.

## Tests

- Tests run offline. They use temporary homes (`--home` or `AMG_HOME`), the fake provider, fake Graph, Gmail, and Jev endpoints, and a fake `credentials.Platform`. A test never reads a real mailbox, calls OpenRouter, or opens the real Keychain.
- To run `amgd` by hand on a temporary home, set `AMG_CREDENTIALS_PASSWORD` before the first command that reads the credential store, and keep the default file backend. All homes share one Keychain item, so `backend = "keychain"` in a temporary home reaches your real Keychain.
- Each behavior change has a test that fails without the change. For a safety check (a refusal, a fail-closed default, redaction, an audit record, or a limit), remove the check once and confirm that the test fails.
- Test data is visibly synthetic: reserved domains such as `example.test`, and tokens such as `sk-or-private-canary`.

## Code and docs

- Read [CODING_STANDARDS.md](CODING_STANDARDS.md) before you write or review a change.
- Name domain concepts with the terms in [GLOSSARY.md](GLOSSARY.md).
- Follow the provider APIs (Gmail API, Microsoft Graph): their options, defaults, and limits. An action should do what a user of that provider's own app expects.
- Treat all text from mail and calendar events as untrusted. Never log or store subjects, senders, bodies, attendee addresses, tokens, or keys.
- Update the docs in the same pull request: the topic page in [`docs/`](docs), the agent skill in [`skills/agentmailguard/SKILL.md`](skills/agentmailguard/SKILL.md), the CLI help, and [SECURITY.md](SECURITY.md) when a boundary changes. Add a line under `Unreleased` in [CHANGELOG.md](CHANGELOG.md) for a change that users notice.
- AI coding agents also read [AGENTS.md](AGENTS.md), which holds the project's safety rules.

## Pull requests

1. Fork the repository and create a branch from `main`.
2. Make one focused change. Keep unrelated cleanup out of it.
3. Write commit messages in the [Conventional Commits](https://www.conventionalcommits.org/) form, for example `fix(gmail): keep the thread of a forwarded draft`.
4. Run `make`, then open the pull request and fill in the template. Say how you tested the change, and which provider behavior you could not check on a real account.
5. A maintainer reviews every pull request. CI must pass before a merge.

## License

By contributing, you agree that your contribution is licensed under the [Apache License, Version 2.0](LICENSE), the license of this project.

## Code of conduct

Everyone who takes part in this project follows the [Code of Conduct](CODE_OF_CONDUCT.md).
