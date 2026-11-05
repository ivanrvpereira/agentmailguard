# Write policy

The write policy in `config.toml` sets, for each account, what the daemon does with each write action.
Each policy key has one of three decisions:

- `allow`: the daemon runs the action now.
- `approve`: the daemon stores the action as a pending action. The action does not run.
- `deny`: the daemon refuses the action.

**Caution:** Do not set `approve` unless the [console password](approval-console.md#console-password) is set.
Only a person in the [approval console](approval-console.md#approval-console) can approve or deny a pending action. Without the password, the console refuses every request and a pending action stays pending.
`amg` and the API cannot approve or deny. A draft change or delete through the API withdraws a waiting send of that draft and never runs or approves anything (see [Waiting sends and draft changes](sending.md#waiting-sends-and-draft-changes)). `approvals status ID` shows the state of a pending action. See [Results](#results).

Example:

```toml
[policy]
calendar_invite = "allow"
calendar_respond = "allow"

[policy.sealed]
mail_trash = "allow"

[accounts.work.policy]
calendar_update = "allow"
calendar_cancel = "allow"

[accounts.personal.policy]
mail_draft = "deny"

[accounts.personal.policy.sealed]
mail_trash = "deny"
```

| Key | Commands | Default |
| --- | --- | --- |
| `mail_trash` | `mail trash`, and rule moves to `trash` | `allow` |
| `mail_spam` | `mail spam` | `allow` |
| `mail_draft` | `draft new`, `draft reply`, `draft forward`, `draft update`, `draft delete` | `allow` |
| `mail_send` | `mail send`, `mail reply`, `mail forward`, `draft send` | `deny` |
| `calendar_invite` | `calendar invite` | `deny` |
| `calendar_update` | `calendar update` | `deny` |
| `calendar_cancel` | `calendar cancel` | `deny` |
| `calendar_respond` | `calendar respond` | `deny` |

For each key, the daemon uses the first value in this order: `[accounts.ALIAS.policy]`, `[policy]`, the default.
To change one key without a hand edit, run for example `amgd config set policy.mail_send approve` or `amgd config set accounts.work.policy.calendar_update allow` on the daemon host. `amgd config unset KEY` removes the value.
These commands have no key, and the policy always allows them: `mail read`, `mail unread`, `mail flag`, `mail categorize`, `mail archive`, and `mail move`.
In `mail organize --apply`, only a rule move to `trash` has a key.

An invalid policy stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, and a reload with an invalid policy is refused.
For example: `config.toml: accounts.personal.policy.sealed.calendar_invite: unknown key`. The error never shows the value.
`amgd config set` refuses an invalid policy key or value and leaves the file unchanged.
A policy is invalid if it has an unknown key or table, a value other than `allow`, `approve`, or `deny`, or a key other than `mail_trash` or `mail_spam` in a sealed table.

## Sealed messages

The daemon always refuses `draft reply`, `draft forward`, `mail reply`, and `mail forward` on a `red`, `sealed`, or `error` message, with the refusal of their key: `refused by policy: mail_draft` or `refused by policy: mail_send`. No setting allows them.
Unless the base decision is `deny`, the daemon looks up the verdict of the message for each reply and forward.

The sealed tables apply only to `mail trash` and `mail spam` and accept only `mail_trash` and `mail_spam`.
The sealed decision is the first value in this order: `[accounts.ALIAS.policy.sealed]`, `[policy.sealed]`, `deny`.
If the message to trash or move to spam is `sealed` or `error`, the daemon uses the stricter of the base decision and the sealed decision.
The order is `allow`, then `approve`, then `deny`.
The daemon looks up the verdict only when the sealed decision is stricter than the base decision.

If the message has no cached verdict, the daemon screens it first. A message that the daemon cannot screen counts as `sealed`.
For replies and forwards of `green` and `yellow` messages, and for `mail trash` or `mail spam` of `green`, `yellow`, and `red` messages, only the base decision applies.
`draft new`, `draft update`, `draft delete`, `draft send`, and `mail send` do not reference a received message, so only the base decision applies. Unless the base decision is `deny`, `draft update`, `draft delete`, and `draft send` first read their message and refuse one that is not a draft, before the Jev action check and any pending action. An approved run reads the message again.

## Jev action check

Before an action runs, Jev checks the text that the agent wrote for it. The agent can be under prompt injection from mail it read, so the daemon does not trust this text.

- Checked actions: `draft new`, `draft reply`, `draft update`, `draft forward`, `mail send`, `mail reply`, `mail forward`, `draft send`, `calendar invite`, and `calendar update`. `calendar respond` and `calendar cancel` are checked only when they have a comment, so `calendar respond --no-notify` is never checked.
- Not checked: `mail trash`, `mail spam`, `draft delete`, rule moves, and the commands without a policy key.
- Jev sees the operation name, a fixed description of what the action does, and the action fields that the agent gave: recipients (for `draft new`, `mail send`, and forwards, To, Cc, and Bcc; for `draft update`, the lists that it gives; for replies, the added Cc and Bcc; for `calendar invite`, also the `--optional` attendees), subject, body (for `calendar update`, the new `--body`; an `--html` body, on mail or meetings, as its HTML source, not rendered), the HTML flag, the reply-all flag, comment, response, and event fields. It never sees a message or event ID. A `draft update` gives only the fields that it changes.
- For `draft reply` and `mail reply`, Jev also sees `reply_recipients`: the To and Cc addresses that the reply goes to, worked out from the original message with the reply rule of [Mail send](sending.md). On Microsoft 365 this list is for the check; Microsoft picks the final list when it makes the reply. If the daemon cannot read them, the check fails.
- For `draft forward` and `mail forward`, Jev also sees the subject and body of the forwarded message, read through the screened read path. If the daemon cannot read this text, for example because the message is not `green`, the check fails.
- For `draft send`, the agent gives only the draft ID, and Jev sees the draft's To, Cc, Bcc, subject, body (HTML as its source), and HTML flag, as the daemon reads them from the provider.

The check comes after the policy decision:

- `deny` refuses the action. Jev does not see it.
- If Jev flags the action, or the check fails, `allow` becomes `approve`, and the action waits in the [approval console](approval-console.md#approval-console). `approve` stays `approve`.
- A missing screening key or a Jev outage makes every check fail, so every checked action waits for approval.
- When a person approves a pending action, the daemon runs it without a second check.

Jev gives a score from 0 to 1. A score of 0.5 or more is a flag. This threshold is provisional and will change after tuning.
The agent never sees the score or the flag. It gets the normal result or the normal `pending` result.
The [approval console](approval-console.md#risk) shows the result as the Risk of the action, for example `▲ 0.62 of 1 · Needs review`, `0.08 of 1 · Checked`, `▲ Check failed · Needs review · TimeoutError`, or `— · Not checked` when no check ran.
If the [fake screening engine](screening.md#screening) made the check, a `fake` tag comes before the value.

## Results

- `allow`: the command gives its normal result.
- `deny`: a draft, send, or meeting command fails with `refused by policy: KEY`, for example `refused by policy: calendar_invite`, with exit code `1`. The action does not run.
- `approve`: a draft, send, or meeting command returns `{"status":"pending","approval_id":"...","url":"http://localhost:8788/a/..."}` with exit code `0`. The action does not run. The `url` is the page of this action in the approval console. It uses `[console].listen` (see [Console configuration](approval-console.md#console-configuration)).

The daemon validates a meeting action before it stores it.
Thus an invalid time, a wrong role, or a recurring or cancelled event fails with the usual error, not `pending`.

`mail trash` and `mail spam` decide each message ID on its own.
The daemon moves the allowed IDs to trash or spam/junk.
A denied ID gets `"ok": false` and `"error": "refused by policy: mail_trash"` or `"error": "refused by policy: mail_spam"`.
An ID that needs approval gets `"ok": false`, `"error": "pending"`, and an `approval_id`, but no `url`. Each such ID is its own pending action.
A conversation trash or spam move (`mail trash --thread` or `mail spam --thread`) decides each chosen message the same way. An approved action moves only the message of its approval, never a message that arrived later.

`approvals status ID` returns `{"state":"..."}` for a pending action. The state is `pending`, `running`, `done`, `denied`, `withdrawn`, or `failed`. A `withdrawn` action is a waiting `draft send` that a draft change or delete withdrew; it never runs.
A `done` action also has `result`: the normal result of the command, except that a `draft new`, `draft reply`, `draft forward`, or `draft update` result keeps only `id` and `thread_id`, and for `draft update` also `send_approval_id`, because the stored result holds no mail content.
The output never shows the stored request arguments.
An unknown ID fails with `approval not found` and exit code `1`.

A rule with `folder = "trash"` follows `mail_trash` for each message.
In the `mail organize --apply` report, the `move.result` of such a message is `pending` or `refused by policy: mail_trash`.
The report does not show the approval ID, and the stats count the message as `failed`.
The message stays incomplete, so each later `--apply` run makes a new pending action for it.

On every provider, `mail move` into trash or spam/junk, or a folder below either one, fails with `move refused: use mail.trash` or `move refused: use mail.spam`, including the common destination names `trash` and `spam`. Use `mail trash` or `mail spam` instead. A rule move to such a folder ID also fails. A rule whose target is `trash` still takes the dedicated trash path and follows `mail_trash`.

On Microsoft 365, `mail folders`, `mail move`, `mail organize`, and `rules list` fail if the daemon cannot look up Deleted Items or Junk Email.

## Storage and audit

Pending actions are in the `pending` table of `cache.db`. They do not expire.
A pending action stores the account key, the operation, and the request arguments: recipients, subject, body, comment, event fields, and the message or event ID.
A waiting draft send stores only the draft ID as its arguments, and beside them, in the `draft` column, the draft fields that Jev checked: To, Cc, Bcc, subject, body, and the HTML flag. A `cache.db` made before this column or the `withdrawn` state existed gets them when the daemon first opens it.
It also stores the result of the Jev check, if a check ran: the flag and the score, or the safe error code, and whether the fake screening engine made the check.
`amgd cache clear` does not delete pending actions. `amgd account remove` denies the pending actions of the removed account.

The audit log gets content-free records for the policy. Their `account` is the account key, and their `operation` is the API operation name, for example `mail.trash`.

- `"event": "policy"` records each decision, also `allow`. It has `time`, `account`, `operation`, `key`, `decision`, and `reasons`: `policy`; `sealed` if the referenced message is `sealed` or `error` and that made the decision stricter; `red` if the replied-to or forwarded message is `red` and that made the decision stricter; `jev_flag` if Jev flagged the action; `jev_error` if the Jev check failed. It can also have `id`, `rule`, and `approval_id`. It never has the Jev score.
- `"event": "approval"` records each step of a pending action. It has `time`, `account`, `operation`, `state` (`created`, `approved`, `denied`, `withdrawn`, `done`, or `failed`), and `approval_id`. A `withdrawn` record is written for each waiting draft send that a draft change or delete withdrew.
- `"event": "console_signin"` records each failed console sign-in: a wrong username or password. It has only `time` and `result` (`failed`). It never has the username, the password, the host name, or the address of the browser.

The operation record of a conversation action also has `thread`, the conversation ID, beside the IDs and results of the chosen messages. Its policy and approval records stay one per message. A conversation ID is an opaque provider ID, not message text.

The audit log also gets content-free records for changes on the daemon host:

- `account_added`, `account_login`, and `account_removed` have `time`, `event`, and `account` (the account key).
- `screening_api_key_set`, `console_password_set`, `store_migrated`, and `store_password_changed` have only `time` and `event`. They never have the secret.
- `config_changed` and `config_removed` have `time`, `event`, and `key`, the dotted key path. A key of an account also has `account`. `config_changed` has `value` only for a key with fixed choices, such as a policy decision, a screening provider, or a backend. Free text, such as an endpoint, a model, or an address, is never in the record.
- `reload` has `time`, `event`, and `result`: `ok` or the refusal code.

A hand edit of `config.toml` writes no `config_changed` record.

## Known limits

The policy is effective only when the agent can use only `amg` commands.
An agent with a shell as the same OS user can run `amgd`, edit `config.toml`, change pending actions in `cache.db`, or restart the daemon.
Anyone who can reach the listener gets the same decisions as the agent.
On Gmail, only the daemon code stops a send. The OAuth grant (`gmail.modify`) permits it.
