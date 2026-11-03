# Approval console

The approval console is a small web page where you approve or deny pending actions.
It always listens, from the start of the daemon. At start, the daemon shows `approval console listening`.
If the console address is taken, the daemon does not start and shows `console listener unavailable`.
Until you set the [console password](#console-password), the console answers every request with HTTP 503 and `console password not set; run amgd config set console.password`. You do not have to restart the daemon after you set the password.

The browser asks for a username and a password. Give the [console username](#console-configuration) (`admin` by default; `amgd config get console.username` shows it) and the [console password](#console-password).

## List page

The list page `/` puts the pending actions in three tables:

- **Messages**: `mail.send`, `mail.reply`, `mail.forward`, `draft.new`, `draft.reply`, `draft.forward`, `draft.update`, `draft.send`, and `draft.delete`. The Action column shows `Send`, `Reply`, `Forward`, `New draft`, `Draft reply`, `Draft forward`, `Edit draft`, `Send draft`, or `Delete draft`.
- **Meetings**: `calendar.invite`, `calendar.update`, `calendar.cancel`, and `calendar.respond`, shown as `Invite`, `Update`, `Cancel`, and `Respond`.
- **Mailbox operations**: `mail.trash`, shown as `Trash`.

Each table shows its pending actions newest first, at most 500 in all.
The page hides a table that has no pending action and no handled action.
If no table is left, the page shows `No actions wait for approval.`
The header shows `N waiting · M need review` for the rows that the filters show.

The columns of each table:

- Messages: Action, Account, To, Message, Score, and Age. To lists the recipients, then `cc:` and `bcc (hidden):` lists. A long address is shortened in the middle of its local part, and the domain stays whole, for example `accounts…@vendor-billing.example.com`. The tooltip shows every address in full. Message shows the subject and a grey one-line preview of the body, at most 200 characters. The preview of an HTML body is its text without the markup.
- Meetings: Action, Account, Event, When, People, Change, Score, and Age. Change shows what the action changes, for example `Decline` with the comment, or `Start` with `10:00 → 11:00`.
- Mailbox operations: Change, Account, Message (the sender and the subject), Received, and Age.

In a window narrower than 1152 pixels, such as on a phone or a tablet, each row shows on three or four lines without column labels, so that the event and the subject always have room.
The Account column shows the account alias.
The tooltip and the open row show the account email.
For a draft send, the recipients, subject, and body are those of the stored draft fields.
A reply row starts its recipients with `reply` or, with `--all`, `reply all`, then the request's `cc:` and `bcc (hidden):` lists.
The action page shows who the reply goes to.

### Live reads

An invitation stores its event, but an update, cancel, or response stores only the event ID, and a trash stores only the message ID.
For these rows, the list reads the event or the message when the page loads, through the same guarded read path that the agent uses (`calendar.get` or `mail.get`).
The list never shows a body.
If the cache has no verdict for the message, the read screens it, as on the action page.
Messages rows never cause a read.
The list reads at most 50 rows per page, at most 4 at a time for one account, and all reads share a deadline of 3 seconds.
If a read fails or is late, the row shows `Event not available` or `Message not available`, and you can still decide it.
For a red or sealed message, the row shows only the sender and `Subject hidden (sealed)`, as the guarded view decides.
Received shows `Today 15:04` for a message of today, otherwise the day, for example `Tue 31 Mar`.

### Score

Each row has a coloured bar.
Messages and Meetings rows also have a Score, the result of the [action check](write-policy.md#jev-action-check):

| Bar | Score | Label | Meaning |
| --- | --- | --- | --- |
| Green | `0.06` | Checked | The check found no risk. |
| Red | `▲ 0.58` | Needs review | The check found a risk. |
| Red | `▲ error` | Needs review | The check failed. |
| Grey | `—` | Not checked | No check ran. |

`mail.trash` never gets an action check, so Mailbox operations rows have no Score and always a grey bar.
The list, Recents, the open row, and the action page show no Score for them.
A Mailbox operations row never needs review.

A score has two decimals and is cut, never rounded, so that a score below the flag threshold never shows as the threshold.
If the [fake screening engine](screening.md#screening) made the check, a small `fake` tag comes before the value.
The list shows only the value.
The open row and the action page show the value and its label, for example `▲ 0.58 · Needs review` or `— · Not checked`.
A failed check has a fixed error code, for example `TimeoutError`.
The list shows it in the tooltip, and the open row and the action page show it after the label.
The footer explains the colours: `Red: needs review · Grey: not checked · Green: checked`.

### Filters

The filter line has two filters, and each option shows the number of pending actions that match it:

- Account: `All`, or one alias (`?account=ALIAS`).
- Show: `All`, `Needs review` (`?status=review`), or `Others` (`?status=others`).

The two filters combine, and the counts of each filter follow the other filter.
The Show filter does not apply to Mailbox operations: under every Show choice, the page shows all their pending actions of the chosen account.
They never need review, so they count under `All` and `Others`, never under `Needs review`.
The header counts the rows that the page shows, so under `Needs review` it includes the Mailbox operations rows, and `M need review` counts only the rows that need review.
If the query has an unknown key, a repeated key, or an unknown value, the page shows the unfiltered list.
It never shows an error page.

### Recents

Each table ends with a closed **Recents** list of its last 10 handled actions: `Done`, `Denied`, `Failed`, or `Withdrawn`.
Recents follows the Account filter, not the Show filter.
A Recents row shows the action, the account, a summary, the Score (not in Mailbox operations), the outcome (a failed action with its error code), and when it was handled.
Each row links to its action page.

### Open rows

Click a row to open it in place.
The open row shows the account email, the Score with its label (not in Mailbox operations), the fields of the live read, and every stored request field as plain text, with `To`, `Cc`, and `Bcc (hidden recipients)` first.
For a draft send, it also shows the draft fields that the action check read.
The body, the invitation text, or the comment shows in full as plain text, under a fixed caption, for example `Body, plain text as the agent wrote it`.
An HTML body shows only as source text, with the caption `Body, HTML source as the agent wrote it, not rendered`.
Links in the text are never links on the page.
The link `Open the action page` opens the [action page](#action-page).

### Decisions from the list

Each pending row has **Approve** and **Deny**.
They show on hover, on keyboard focus, on touch, and in an open row.
A row that needs review has **Review** and **Deny**.
Review opens the action page.
In a window narrower than 1152 pixels, the row buttons show only in an open row, and Review does not show there.
To approve a row that needs review from the list, open the row, tick `I reviewed it`, and press Approve.
The server refuses an approve of an action that needs review without that box with HTTP 400 and `review required`, and nothing runs.

Each pending row has a checkbox, and each table has a header checkbox that selects every row of the table.
Without the script, the header checkbox is a link (`?select=messages`, `?select=meetings`, or `?select=mailbox`) that shows the page with the rows of the table ticked.
While any box is ticked, a bulk bar shows under the filters.
It shows `N selected`, `M need review: Approve skips them` when the selection has rows that need review, and the buttons **Approve selected**, **Deny selected**, and **Clear**.
If every selected row needs review, Approve selected is off, and the bar shows `All need review: approve each from its row`.

A batch decides at most 500 pending actions, one after another, each through the same approval path as a single decision:

- Approve selected skips the Messages and Meetings rows that need review. It never skips a Mailbox operations row.
- Deny selected denies every selected pending action.
- An action that is no longer pending is skipped and counted as already handled. Nothing runs twice.
- A failed action is counted as failed. Nothing is retried.

After a batch, the list shows a notice, for example `3 approved, 1 skipped (needs review), 1 failed, 2 already handled`.
A decision from the list goes back to the list with its filters.
A decision from the action page goes back to the action page.

### Auto-refresh

The Auto-refresh menu reloads the list every `5 minutes` (the default), `10 minutes`, `30 minutes`, or `1 hour`, or never (`Off`).
The choice (`?refresh=5m`, `10m`, `30m`, `1h`, or `off`) goes to the cookie `amg_console_refresh` for one year, and the page then goes back to the list with its filters.
The cookie holds only the interval.
With the script, the page does not reload while a box is ticked, a row or a Recents list is open, or the menu is open.
When the interval is up, it reloads after the page is idle for 5 seconds, or for the interval if that is shorter.
A reload keeps the filters, but no ticked table and no batch notice.
Without the script, the page reloads at the same interval through a `<noscript>` refresh.

## Action page

The action page `/a/ID` shows one action: its label and operation, the state, the account, the times, the Score with its label (not for `mail.trash`), and every stored request field as plain text under **Request**.
`Bcc (hidden recipients)` lists people who get the message, but the other recipients do not see them.
A `done` action shows its result, and a `failed` action its error code.

- For an action on an existing message, the page shows under **Message** the verdict, sender, subject, and received date of the message. For an action on an event, it shows under **Event** the verdict, subject, start, end, and organizer. The daemon reads these through the guarded read path, which screens a message that has no verdict yet, and never shows a body. If the read fails, the page shows `Message not available.` or `Event not available.`, and you can still decide.
- For a reply, the page also shows the recipients that the reply goes to under **Reply recipients**: the To and Cc worked out from the original message with the reply rule of [Mail send](sending.md), plus the request's `cc`, and the request's `bcc` as `Bcc (hidden recipients)`. The daemon reads the original when the page loads. If it cannot, the page shows `Reply recipients could not be read.` On Microsoft 365, Microsoft picks its own list when it makes the reply. It should match.
- For a draft send, the page also shows under **Draft** the draft fields that the action check read, stored when the send began to wait: To, Cc, Bcc (hidden recipients), subject, and the body under its caption. The send sends the draft as it is at the provider when it runs, so a later change that the user makes in the mail app is not on the page. A change or delete of the draft by the agent withdraws the action instead (see [Waiting sends and draft changes](sending.md#waiting-sends-and-draft-changes)). If the draft is gone or is no longer a draft when the run starts, the run fails with a safe code and nothing is sent.

The body, the invitation text, or the comment shows in full as plain text under its caption.
An HTML body also shows as a Rendered preview above its source, with the caption `Rendered preview: links, images, and forms are off. Hidden text shows only in the source.`
The preview runs no scripts, loads nothing remote, submits no forms, and opens no links.
Approve and Deny stay outside it.
The source below the preview shows the text that the preview hides.

A pending action has **Approve** and **Deny** buttons.
If the action needs review, Approve needs the box `I reviewed it`.

**Approve** runs the stored action once, with no second policy check.
A `done` action shows its result.
If the run fails, the action shows `failed` and a safe error code.
A failed action never runs again.
**Deny** stops the action for good.
Nothing runs.
A `withdrawn` action has no buttons and never runs.
A decision on an action that is already handled, also a `withdrawn` one, shows `This action was already handled. Nothing ran again.`, and nothing runs.

## Console script

The pages load one small script, `/assets/console.js`, from the console itself.
The script adds convenience only.
Every page works without it, and the server enforces every rule.
The script does these things:

- It pauses Auto-refresh while you work.
- It turns each header link into a checkbox that shows checked or partly checked.
- It keeps Approve off until you tick `I reviewed it`.
- It adds a screen-reader line with the selection count.
- It puts the waiting count in the tab title, for example `(4) AgentMailGuard · Web console`.

It never reads row text, never writes HTML, and never calls `fetch` or `eval`.

## Use from a phone

To use the console from a phone, put it behind an HTTPS tunnel, for example `tailscale serve`, and add the tunnel host name to `[console].hosts`.
Keep `listen` on loopback.
If `listen` is not `localhost` or a loopback address, the daemon shows a warning at start.

## Protections

- Every page and the script need the username and the password (HTTP Basic authentication). The console has no TLS of its own.
- The console refuses a request whose `Host` is not `listen` or an entry of `hosts`, also before a password exists. This blocks DNS rebinding.
- Every POST (a decision on `/a/ID` and a batch on `/batch`) needs an `Origin` of the console itself. `Origin: null` is refused. This blocks form posts from other sites. The console pages send `Referrer-Policy: same-origin`, so that the browser sends that `Origin` on the console's own form posts. With `no-referrer`, it would send `Origin: null`.
- Every POST needs the type `application/x-www-form-urlencoded`, only the known fields, and a body of at most 4 KiB for a decision or 32 KiB for a batch. Otherwise, the console refuses it with HTTP 400 and `invalid request`.
- The return field of a decision is accepted only as `/` or `/?` and a valid list filter, at most 512 bytes. The console builds the redirect from the filter and never from the field, so it is never an open redirect.
- After 5 failed sign-ins (a wrong username or password) in one minute, the console refuses every request with HTTP 429 until the oldest failure is more than one minute old.
- Each failed sign-in adds a `console_signin` record to the audit log.
- The content security policy allows only the console's own script, no remote resources, and form posts only to the console. Trusted Types make every HTML write from a script fail.
- The console shows every text from the agent or from mail as literal text.
- Browsers must not cache or frame the pages.

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
