# Approval console

The approval console is a small web page where you approve or deny pending actions.
It always listens, from the start of the daemon. At start, the daemon shows `approval console listening`.
If the console address is taken, the daemon does not start and shows `console listener unavailable`.
Until you set the [console password](#console-password), the console answers every request with HTTP 503 and `console password not set; run amgd config set console.password`. You do not have to restart the daemon after you set the password.

The browser asks for a username and a password. Give the [console username](#console-configuration) (`admin` by default; `amgd config get console.username` shows it) and the [console password](#console-password).

- The list page `/` shows all pending actions, newest first, and then the 50 most recently changed other actions, `withdrawn` actions among them. Each row shows the account, the operation, the recipients (To, then `cc:` and `bcc (hidden):` lists), the subject, the state, and the age. For a draft send, the recipients and subject are those of the stored draft fields. A reply row starts its recipients with `reply` or, with `--all`, `reply all`, then the request's `cc:` and `bcc (hidden):` lists; the action page shows who the reply goes to.
- The action page `/a/ID` shows the account, the operation, the state, the times, and every stored request field as plain text, with `to`, `cc`, and `bcc` first. `bcc` is labeled `bcc (hidden recipients)`: these people get the message, and the other recipients do not see them. An `--html` body is labeled `body (HTML source, not rendered)` and shows as source text. Links in the text are never links on the page.
- For a reply, the action page also shows the recipients that the reply goes to under **Reply recipients**: the To and Cc worked out from the original message with the reply rule of [Mail send](sending.md), plus the request's `cc`, and the request's `bcc` as `bcc (hidden recipients)`. The daemon reads the original when the page loads; if it cannot, the page shows `Reply recipients could not be read.` On Microsoft 365, Microsoft picks its own list when it makes the reply; it should match.
- For a draft send, the action page also shows under **Draft** the draft fields that Jev checked, stored when the send began to wait: `to`, `cc`, `bcc (hidden recipients)`, `subject`, and `body` (an HTML body as `body (HTML source, not rendered)`), as plain text. The send sends the draft as it is at the provider when it runs, so a later change that the user makes in the mail app is not on the page. A change or delete of the draft by the agent withdraws the action instead (see [Waiting sends and draft changes](sending.md#waiting-sends-and-draft-changes)). If the draft is gone or is no longer a draft when the run starts, the run fails with a safe code and nothing is sent.
- For an action on an existing message, the action page also shows the sender, subject, received date, and verdict of the message. For an action on an event, it shows the subject, start, end, and organizer. The daemon reads these through the screened read path and never shows a body. If the read fails, the page shows `not available`, and you can still decide.
- The action page also shows the result of the [Jev action check](write-policy.md#jev-action-check), if a check ran.
- A pending action has **Approve** and **Deny** buttons.

**Approve** runs the stored action once, with no second policy check. A `done` action shows its result. If the run fails, the action shows `failed` and a safe error code. A failed action never runs again.
**Deny** stops the action for good. Nothing runs.
A `withdrawn` action has no buttons and never runs.
An action that is already handled, also a `withdrawn` one, shows `already handled`, and nothing runs.

To use the console from a phone, put it behind an HTTPS tunnel, for example `tailscale serve`, and add the tunnel host name to `[console].hosts`.
Keep `listen` on loopback. If `listen` is not `localhost` or a loopback address, the daemon shows a warning at start.

Protections:

- Every page needs the username and the password (HTTP Basic authentication). The console has no TLS of its own.
- The console refuses a request whose `Host` is not `listen` or an entry of `hosts`, also before a password exists. This blocks DNS rebinding.
- Approve and Deny need an `Origin` of the console itself. This blocks form posts from other sites. The console pages send `Referrer-Policy: same-origin`, so that the browser sends that `Origin` on the console's own form posts; with `no-referrer` it would send `Origin: null`.
- After 5 failed sign-ins (a wrong username or password) in one minute, the console refuses every request with HTTP 429 until the oldest failure is more than one minute old.
- Each failed sign-in adds a `console_signin` record to the audit log.
- Pages have no scripts and no remote resources, and browsers must not cache or frame them.
## Console configuration

The optional `[console]` table in `config.toml` sets the address of the [approval console](#approval-console) and the username of its sign-in.

```toml
[console]
listen = "localhost:8788"
hosts = ["my-mac.example.ts.net"]
username = "admin"
```

- `listen`: `HOST:PORT`. The default is `localhost:8788`. The console listens on this address, and the pending `url` uses it.
- `hosts`: extra host names that the console accepts, for example a Tailscale name. Each entry is a host name or IP address with an optional `:PORT`. An entry without a port matches a request without a port. Put an IPv6 address in brackets. The default is none.
- `username`: the username that the console sign-in needs, with the [console password](#console-password). The default is `admin`. It has 1 to 64 characters: ASCII letters, digits, `.`, `_`, `-`, and `@`. It is not a secret.

`amgd config set console.listen HOST:PORT`, `amgd config set console.hosts NAME1,NAME2`, and `amgd config set console.username NAME` change the table.
A new `hosts` list and a new `username` take effect at the reload. A new `listen` address takes effect only when you start the daemon again: the command prints `restart needed: console.listen changed`.
An invalid table stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, for example `config.toml: console.listen: must be HOST:PORT`. The error never shows the value.

## Console password

The console password protects the [approval console](#approval-console). Until you set it, the console refuses every request.

Run `amgd config set console.password` on the daemon host to set or change it.
The command does not ask for the current password, so you can also reset a forgotten password with it.
On a terminal, echo is off and it asks for the new password twice.
Non-terminal stdin supplies the lines in the same order without a confirmation: the file backend password (if `AMG_CREDENTIALS_PASSWORD` is absent), and then the new password.
The password must be 8 to 72 bytes of valid UTF-8 without control characters.
The command stores only a bcrypt hash of the password in the credential store (Keychain or the encrypted file). It never prints the password or the hash, and it does not accept them as arguments.
It prints `Console password saved. Sign in to the console as USERNAME (console.username).`, writes a `console_password_set` audit record, and reloads the daemon. The new password takes effect at the reload, and the old password stops working at once. Make sure that the daemon log shows `reload ok`: after `reload refused`, the old password stays valid until a reload succeeds.

The daemon reads the hash at start and at each reload. It shows `credential check: console password: ok` or `not set; the console refuses every request until amgd config set console.password`. A missing password does not stop the start.
