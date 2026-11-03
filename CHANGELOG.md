# Changelog

All notable changes to this project are listed here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).
Until version 1.0.0, a minor version can change behavior that users rely on.

## [Unreleased]

The first public release.

### Changed

- Trash and spam/junk, including their subfolders, are closed to agents on every provider: no reads, replies, draft changes, or moves out; conversation and search results omit their messages. Moves into these folders are refused; only `mail trash` and `mail spam` can put mail in trash and spam/junk.

### Added

- `amg mail spam IDS... | --thread THREAD_ID [--in FOLDER_ID]` moves open messages to spam/junk under the new `mail_spam` write policy (default allow; sealed/error default deny), with per-message approvals.
- `amgd`, a daemon that screens Microsoft 365 and Gmail mail and calendar events for AI agents, and `amg`, the agent's client, which talks to it over HTTP.
- Screened mail reads: list, search, read, and whole conversations. Each message gets a verdict (`green`, `yellow`, `red`, `sealed`, or `error`) that decides what the agent sees.
- Mail changes by message or by conversation: archive, move, trash, flag, categorize, and read state.
- Drafts, sends, replies, reply all, and forwards that keep the original's attachments, with Cc, Bcc, and HTML bodies; draft update, delete, and send.
- Calendar reads and search, meeting invitations with Teams or Google Meet links, updates, cancellations, responses, and Microsoft 365 availability.
- A write policy for each account, an action check of the text that the agent writes, and a password-protected approval console for actions that wait.
- The approval console groups pending actions into Messages, Meetings, and Mailbox operations, with a Score on each Messages and Meetings row, filters by account and review state, Recents, and decisions from a row or in a batch, and an action that needs review waits for `I reviewed it`.
- The console list refreshes by itself and pauses while you work, the action page shows a sandboxed Rendered preview of HTML bodies, and the console works without its small script.
- Organizing rules in `rules.toml`, with a preview before any change.
- Several accounts in one daemon, keyed by email, with browser, device-code, and headless login.
- Credential storage in the macOS Keychain or a password-encrypted file, settings commands that reload the daemon, and a content-free audit log.
- A fake provider and fake screening, which judges by keywords, for tests and demos.
- `--version` on both programs, signed and notarized macOS binaries, static Linux binaries, and a Homebrew formula. Each release archive has build provenance and the license notices of the code it contains.

[Unreleased]: https://github.com/ivanrvpereira/agentmailguard/commits/main
