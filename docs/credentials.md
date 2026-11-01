# Credential storage

Both backends store all provider OAuth credentials, the screening key, and the [console password](approval-console.md#console-password) hash in one versioned record.
Only `amgd` reads the store. `amg` has no store code.
The optional configuration table selects the backend:

```toml
[credentials]
backend = "file" # Default. Use "keychain" for the macOS Keychain.
```

An absent table selects `file`.
Linux supports only `file` because the native Keychain backend is macOS-only.
On macOS, run `amgd config set credentials.backend keychain` on a new home before the first `amgd account add` to use the Keychain.
Earlier versions selected `keychain` when the table was absent. A home that keeps its record in the Keychain must now set `backend = "keychain"`, or copy the record to the file backend as described in [Copy credentials to the other backend](#copy-credentials-to-the-other-backend).
Any other key or value is a configuration error.
Neither backend detects rollback: another process under the same user can restore an older valid credential record.

## Keychain backend

The Keychain backend uses one non-synchronizable generic-password item: service `agentmailguard`, account `credentials`.
All configured accounts share this item under the current operating-system user, with separate entries keyed by provider and email.
All homes of the user also share this item.
When `amgd` asks for access, choose "Always Allow" to avoid repeated access prompts for the same build.
A rebuilt binary needs approval again.

## File backend and password input

The file backend stores the record in `<home>/credentials.age` with mode `0600`.
It uses age password encryption, and writes are atomic.
A wrong password or damaged file refuses access with the same fixed error.
Unknown record versions also refuse access.

The password source is `AMG_CREDENTIALS_PASSWORD`, then stdin if the variable is absent.
An empty password is refused, including an explicitly empty environment variable.
Passwords must be valid UTF-8, at most 4096 bytes, without control characters.
Terminal input disables echo. Creation of a new file asks for the password twice.
Non-terminal stdin supplies one password line without confirmation.
The process keeps the password in memory for its lifetime.

**Caution:** Environment variables can expose secrets to other processes under the same user.
The program unsets the password variable after reading it, before it starts child processes such as a browser.
Unsetting the variable does not remove the initial environment exposure or erase the password from process memory.
Do not put passwords in command arguments, shell history, or configuration files.
A secret manager can supply the environment variable or stdin.
A password that starts with `op://` is refused: it is an unresolved 1Password secret reference, not a password. Run `amgd` through `op run` so that 1Password resolves the reference first.

Run `amgd config set screening.api_key` on the daemon host to save the screening key.
Terminal input disables echo. Non-terminal stdin supplies the key as one line.
With the file backend and no password environment variable, piped input must contain two lines: the password first, then the key.
With the password environment variable, piped input contains only the key line.
The command refuses keys with spaces or control characters. It never prints the key or accepts it as an argument.
It writes a `screening_api_key_set` audit record without the key, and reloads the daemon.

The daemon reads the screening key only from the credential store. It ignores `OPENROUTER_API_KEY`, so a key set for other tools is never sent to a configured endpoint.
No `amg` command transfers a provider token or screening key to the daemon.

## Copy credentials to the other backend

1. Stop the daemon.
2. Make sure `[credentials] backend` names the source backend. To copy from the Keychain on a home without the table, run `amgd config set credentials.backend keychain` first, because an absent table selects `file`.
3. Run one command on the daemon host:

```sh
amgd store migrate --to file
# Or, from a configured file backend on macOS:
amgd store migrate --to keychain
```

4. Start the daemon with the selected backend.

Migration copies the whole record from the configured backend to the other backend under the same-home lock.
Then it sets `[credentials] backend` in `config.toml` to the new backend and prints `restart needed: credentials.backend changed`.
It leaves the source record unchanged.
It refuses while a daemon runs for the home: `a daemon runs for this home; stop it first`.
It refuses an existing target record. It also refuses a target that matches the configured backend.
It writes the audit records `store_migrated` and `config_changed`.
The file password comes from the normal sources. Terminal creation of the file target asks twice.

## Change the file password

1. Stop the daemon.
2. Run `amgd store change-password` on a terminal on the daemon host.
3. Supply the current password through its normal source.
4. Enter the new password twice at the terminal prompts.
5. Start the daemon with the new password.

This command works only with the file backend and re-encrypts the latest record under the same-home lock.
It refuses while a daemon runs for the home.
The new password cannot come from an environment variable or a pipe.
It writes a `store_password_changed` audit record.
