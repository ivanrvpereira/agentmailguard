# Changelog

All notable changes to this project are listed here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).
Until version 1.0.0, a minor version can change behavior that users rely on.

## [Unreleased]

The first public release.

### Changed

- `amgd serve` prints the approval console URL with its username and `console.hosts`, the API URL with the `AMG_HOST` line for agents, and, for a non-loopback listener, a warning after the address that names it, the `mail_send` decisions, and the fixes. `daemon listening` is now `daemon listening: http://ADDRESS`.
- `amgd serve` does not start without a console password. On a terminal it asks for one; without a terminal it refuses to start and names `amgd config set console.password`, with `--home HOME` when it got `--home` or `AMG_HOME`. Before, the daemon started and its console refused every request.
- Without a daemon, `amgd` setting changes print `no daemon runs for this home. Now run: amgd serve`, with `--home HOME` when the command got `--home` or `AMG_HOME`.

### Added

- A systemd unit and a launchd plist in each release archive (`service/`) and in Homebrew's `share/agentmailguard/service`. The systemd unit reads the credential file password from an encrypted systemd credential and shows the daemon only its home under `/home`. "Run as a service" in `docs/setup.md` has the whole first start.
- Microsoft 365 login works on headless Linux: `amgd account add|login --no-browser` prints the sign-in URL and takes the redirected `http://localhost:PORT/?code=…` address as a paste, with the checks of the loopback callback. Browser login opens the URL with `xdg-open` on Linux, always prints it, and keeps waiting when no browser opens. A refused device-code sign-in names `--no-browser`, since many organizations block device code.
- `amgd`, a daemon that screens Microsoft 365 and Gmail mail and calendar events for AI agents, and `amg`, the agent's client, which talks to it over HTTP.
- Screened mail reads: list, search, read, and whole conversations. Each message gets a verdict (`green`, `yellow`, `red`, `sealed`, or `error`) that decides what the agent sees.
- Mail changes by message or by conversation: archive, move, trash, flag, categorize, and read state.
- Drafts, sends, replies, reply all, and forwards that keep the original's attachments, with Cc, Bcc, and HTML bodies; draft update, delete, and send.
- Calendar reads and search, meeting invitations with Teams or Google Meet links, updates, cancellations, responses, and Microsoft 365 availability.
- A write policy for each account, an action check of the text that the agent writes, and a password-protected approval console for actions that wait.
- The approval console groups pending actions into Messages, Meetings, and Mailbox operations, with a Risk (the action check score) on each Messages and Meetings row, filters by account and review state, Recents, and decisions from a row or in a batch, and an action that needs review waits for `I reviewed it`.
- The console list refreshes by itself and pauses while you work, the action page shows a sandboxed Rendered preview of HTML bodies, and the console works without its small script.
- Organizing rules in `rules.toml`, with a preview before any change.
- Several accounts in one daemon, keyed by email, with browser, device-code, and headless login.
- Credential storage in the macOS Keychain or a password-encrypted file, settings commands that reload the daemon, and a content-free audit log.
- A fake provider and fake screening, which judges by keywords, for tests and demos.
- `--version` on both programs, signed and notarized macOS binaries, static Linux binaries, and a Homebrew formula. Each release archive has build provenance and the license notices of the code it contains.

[Unreleased]: https://github.com/ivanrvpereira/agentmailguard/commits/main
