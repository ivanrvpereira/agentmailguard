# Gmail manual login

## Status

Implementation, independent review, and final verification are complete on branch `gmail-manual-login`, based on `215c176`.
Commit subject: `feat(auth): add manual Gmail login`.
Do not push or merge without user approval. The feature is not deployed to Hermes.

## Approved scope and result

`agentmailguard --account gmail auth login --manual` prints the Google authorization URL, accepts the full callback URL on stdin, and completes login without a browser launch or SSH tunnel.
The prompt hides terminal input and consumes one newline-terminated callback line of at most 4096 bytes.
Manual mode is Gmail-only, incompatible with device-code mode, and subject to the existing local-only login restrictions and five-minute provider timeout.
Scopes, PKCE, constant-time state validation, token exchange, initial refresh validation, credential storage, and default browser login remain intact.
The callback must match the generated loopback scheme, host, port, and path. Userinfo, fragments, malformed queries, duplicate security fields, and OAuth errors are refused. The pasted URL is never fetched.
The server still requires an operator restart after login. No automatic restart, deployment, SSH helper, or dependency was added.

## Verification evidence

- Baseline and final `make` passed formatting, vet, race tests, and builds. `git diff --check` passed.
- Parent reran `go test -race ./internal/cli ./internal/providers/gmail -run 'Test(Manual|BrowserLogin|FileTerminalPassword)' -count=1`; both packages passed.
- Baseline binary refused `auth login --manual --help` with exit 2. The native manual-login fixture failed before its password prompt because the flag did not exist.
- Implementation worker recorded red/green at exported `gmail.Client.Login` and `cli.Run` seams for manual PKCE/refresh, oversize callback refusal, usable encrypted identity, and partial EOF refusal.
- Independent reviewer demonstrated that removing state validation or accepting partial EOF makes the corresponding public-boundary tests fail. Both tests passed with the guards intact.
- Parent exercised the final built CLI in an isolated Linux container with Go 1.26.3. The fixture used a synthetic non-forwarding TLS provider, a temporary encrypted home, and a real PTY. Successful login validated PKCE and refresh. Wrong state, EOF, and Ctrl+C preserved the existing encrypted credentials and did not reach the provider. Password/callback input did not echo, terminal state was restored, and a temporary server restart returned successful Gmail `auth status`.
- Parent executed CLI help and local-only/manual-device refusal checks through fish. Help lists `--manual`; invalid combinations return the expected exit codes.
- No real provider calls, user credential-home/Keychain access, or Hermes changes occurred for this implementation.

## Decisions and limits

The existing boundary options and injected stdin seams were reused. `gmail.Options.ReadCallback` selects manual input without changing the exported login signature.
An arbitrary blocked reader can retain one goroutine until input closes; cancellation still completes the one-shot CLI promptly. No retry machinery was added.
The macOS native fixture could not trust its synthetic certificate through `SSL_CERT_FILE`. Native verification moved to an isolated Linux container without Keychain changes or weakened TLS.
The native verification fixture is `/tmp/agentmailguard-verify-manual-login.py`. Login, PKCE/refresh, hidden input, terminal restoration, refusal safety, and restart/status passed. It made zero real provider calls.
Real Google consent and authentication remain unverified. The user must complete that flow after deployment.
