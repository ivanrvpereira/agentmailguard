# Implementation handoff

## Baseline

- Branch `credential-backends`, worktree `../agentmailguard-go-credentials`, forked from `main` at `96a1d13`.
- Baseline `make -C /Users/ivanpereira/code/ivanrvpereira/agentmailguard-go-credentials` passed: gofmt, vet, all race tests, and build. Existing uncommitted files were `.gitignore`, `PLAN.md`, and `PROGRESS.md`; preserve the plan and ignore-file edits.
- Go `go1.27.1 darwin/arm64`, CGO enabled. The user's shell is fish; use `make -C` or `builtin cd` in walkthroughs, because the user's `cd` function calls an unavailable `z`.
- The port's history is in `.backup/PROGRESS.md` (ignored).
  - M365 live reads were unblocked by the user's decision to accept extra grants with a warning.
  - Native Keychain integration remains unverified by automated tests.
  - Source docs are stale in places; current source and tests define behavior.
- Workers and tests use fake servers, fake credential platforms, and temporary state only. No live provider, real Keychain, or user configuration access.

## Steps

1. Single-record store and Keychain backend: complete. Separate review, independent verification, and step commit recorded.
2. File backend: pending.
3. `auth migrate` and `auth change-password`: pending.
4. Docs and complete workflow: pending.

## Current code facts

- `internal/credentials/credentials.go`: unchanged `Store` interface over a versioned, fmt-redacted `Record` and whole-record `Backend`. `RecordStore` serializes reads, migration, and reread-before-save updates with a cancellable home Flock. Errors and panics never retain backend text or failure chains.
- `internal/credentials/keychain.go`: one item, service `agentmailguard-go`, account `credentials`. When absent, the first operation migrates all configured legacy accounts and the Jev key. Strict decoding refuses malformed records before any write; legacy items remain unchanged. Only `nil, nil` from `Platform.Read` means absent.
- `config.LoadAccounts` validates all accounts from one parse. Production server and local CLI construction pass the home and full account list. `auth set-jev-key` now accepts `--home`; other local-only restrictions remain.
- `internal/credentials/keychain_darwin.go`: `nativeKeychain` uses `keybase/go-keychain`. It creates items not synchronizable, with `AccessibleWhenUnlockedThisDeviceOnly` (ignored by the legacy file-based keychain), and updates in place on a duplicate.
- `internal/server/preload.go`: the production store supplies one record load/rewrite before memory-only runtime loads. Saves reread the backend under lock before updating memory, so another local command's entries survive. Plain injected `Store` implementations retain the existing per-account checks. Missing logins still do not block startup.
- `internal/server/adapters.go`: `safeStore` converts adapter panics to errors.
- `internal/cli/local.go`: `serve` calls `preload` before `server listening`. `internal/cli/jev_key.go` has `readJevKey` and `terminal` (a `stty -echo` prompt, with no terminal dependency).
- `internal/server/native_test.go`: the native binary test currently checks only the no-configuration refusal, because reaching the listener would touch the real Keychain. Step 2 restores native serve coverage with the file backend.
- The M365 provider rereads the store at every access-token expiry, and both providers save after every refresh.

## Decisions and research

- **Library choice.** No maintained Go library covers both backends well.
  - `99designs/keyring` is unmaintained since 2022. `byteness/keyring` is the active fork (13 backends, including `file`). Both use a file backend with jose2go PBES2-HS256 (PBKDF2, reported 8192 default iterations), one file per secret, a plain `os.WriteFile`, and no lock.
  - `zalando/go-keyring` has no file backend and runs `/usr/bin/security`, which limits an item to about 2.2 KB of JSON.
  - Chosen: keep `keybase/go-keychain` for the Keychain, `filippo.io/age` (scrypt passphrase mode, audited format) for the file, and stdlib `syscall.Flock` for locking.
- **Security comparison.** A fable 5.1 analysis compared secrets stored directly in the Keychain with a Keychain-held key encrypting a file.
  - They are equivalent against same-user processes, offline theft, and root.
  - The key-plus-file variant adds two-store consistency risk and an undetected rollback. It helps only when the Keychain locks on sleep or the record is large.
  - Hence one Keychain item holding the record directly.
- **Why prompts repeat.** The binary is ad-hoc signed (`codesign -dv`: `Signature=adhoc`, designated requirement = cdhash), so each rebuild is a new app to the Keychain ACL. gogcli (`openclaw/gogcli`) trusts the application only for binaries with a stable Team ID for the same reason. Stable signing is out of scope here.
- **User decisions (2026-09-29).**
  - Use "Always Allow" with one Keychain item.
  - Support a password file backend.
  - Password from the env var and from stdin.
  - The user deletes legacy items after migration.
  - Approved: the `AGENTS.md`/plan policy change and the `filippo.io/age` dependency.

## Verification limits

- Final production-binary migration cannot use the fake `Platform`: the native adapter calls Security APIs directly. Approval requested to use the approved constructor seam for migration verification instead. No real Keychain access is authorized for new code.
- Other native workflow checks can use a non-forwarding loopback HTTPS proxy and a temporary CA. The installed Go 1.27 supports `GODEBUG=x509sslcertoverrideplatform=1` with `SSL_CERT_FILE` on macOS. Configure these only on temporary child processes. This approach is source-verified, not yet run.

## Verification evidence

- Baseline: `make -C /Users/ivanpereira/code/ivanrvpereira/agentmailguard-go-credentials` passed before implementation.
- Baseline native CLI, run from fish with temporary `HOME` and `AGENTMAILGUARD_GO_HOME`: `--help` succeeded; `serve --listen 127.0.0.1:0 </dev/null` refused missing configuration with exit 1. No Keychain access occurred.

### Step 1

- Workers observed red/green at the approved constructor, `server.New`/`Preload`, and `cli.Run` seams. Focused commands were `go test ./internal/credentials -run '<selection>' -timeout 60s`, `go test ./internal/server -run TestRecordPreload -timeout 60s`, and `go test ./internal/cli -run TestLocalCommandsMigrateBeforeSaving -timeout 60s`. Reds exposed omitted migration entries, partial writes, concurrent lost updates, ignored locks, malformed/version-alias acceptance, five startup reads/two writes, and rejection of `--home`. Each selection passed after its implementation.
- Reviewer regressions also failed before fixes: `TestPlatformErrorsAreNeverTreatedAsMissingItems`, `TestMigrationFailsClosedWithoutWritingPartialRecord`, `TestBackendPanicsAndFailureChainsStayPrivate`, and `TestMigrationRefusesPresentButEmptyLegacyKey`. Their green reruns confirmed fail-closed migration and fixed failures without secret text.
- Orchestrator read the final production and test diff. `make -C /Users/ivanpereira/code/ivanrvpereira/agentmailguard-go-credentials` passed; uncached `go test -race ./internal/credentials ./internal/cli ./internal/server -count=1 -timeout=60s` passed from fish. This exercised fake login, migration, cross-process locking, preload, HTTP auth status, refresh after an external key update, and failed-save memory preservation.
- Built-binary checks from fish with clean child environments and temporary HOME passed: `--help`; missing-config refusal for `serve --listen 127.0.0.1:0` and `auth set-jev-key --home <temp>` (exit 1); remote-target login refusal (exit 1); account-target key-setup refusal (exit 2).
- Native Keychain permissions and migration remain unverified. Locks coordinate only processes using the same home, as specified in the plan.
