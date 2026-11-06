# HTTP interface and limits

The only data endpoint is `POST /v1/call` with `Content-Type: application/json`:

```json
{"operation":"mail.list","account":"work","params":{"folder":"inbox","count":10}}
```

Operation names match `amg` command groups, such as `mail.get`, `mail.send`, `draft.update`, `calendar.invite`, `account.list`, `account.status`, and `approvals.status`.
`account.list` returns the alias, provider, email, and `default` flag of each account, with no secrets. `account.status` returns the sign-in state, email, and scopes of one account.
The API has no operation that changes credentials, the console password, accounts, or settings. A test compares the operation table with a reviewed list, so a new operation needs a review.
`mail.thread` takes `{"id":"THREAD_ID"}`, and `calendar.search` takes `{"text":"TEXT"}` and an optional `page`.

The common folder names `inbox`, `trash`, and `spam` work on every provider, in any case, in `mail.list`, `mail.search`, and `in`. Any other value is the provider's folder or label ID, as `mail.folders` lists it, such as Gmail `SENT`. `mail.move` into trash or spam/junk, or a folder below either one, fails with `move refused: use mail.trash` or `move refused: use mail.spam`, also for the common names. Only `mail.trash` and `mail.spam` put messages there.
Meeting times on the HTTP interface are RFC 3339 timestamps. `calendar.update` also accepts a Go `duration` string.
Date bounds on the HTTP interface are exact RFC 3339 times, never days: `mail.search` `after` (included) and `before` (excluded, later than `after` when both are given), `mail.organize` `since` (required, not later than the daemon clock) and `until` (excluded, later than `since`), and `calendar.list` `start` (included) and `end` (excluded, later than `start`). A date without a time is refused. The CLI computes these times from days, as [Commands and output](commands.md#commands-and-output) describes.
`calendar.invite` takes an optional `optional` address list, `calendar.update` an optional `body`, and `calendar.respond` an optional boolean `no_notify`.
`calendar.invite` and `calendar.update` take an optional boolean `html` that marks `body` as HTML; a `calendar.update` with `html` and without `body` is refused as `invalid request`.
`draft.new` and `mail.send` take the optional address lists `to`, `cc`, and `bcc`, the optional strings `subject` and `body` (empty when absent), and the optional boolean `html`. `draft.reply` and `mail.reply` take the message `id`, the optional string `body` (empty when absent), the optional booleans `html` and `all`, and the optional address lists `cc` and `bcc`. `draft.forward` and `mail.forward` take the message `id`, the optional address lists `to`, `cc`, and `bcc`, the optional string `body` (empty when absent), and the optional boolean `html`. `mail.send` and `mail.forward` without an address in `to`, `cc`, or `bcc` are refused as `invalid request` before any policy decision. `draft.update` takes the message `id` and at least one of the address lists `to`, `cc`, and `bcc`, the string `subject`, and the string `body`, with the optional boolean `html` only together with `body`; an absent param stays unchanged and an empty list clears. A `draft.update` without a field is refused as `invalid request`. `draft.delete` and `draft.send` take only the message `id`.
`mail.archive`, `mail.trash`, `mail.spam`, `mail.read`, `mail.unread`, `mail.move`, and `mail.categorize` take either a non-empty `ids` list or `thread`, a conversation ID, never both; `in`, a folder ID, goes only with `thread` and defaults to `inbox`, as for `mail.list`. `mail.flag` takes neither `thread` nor `in`. A repeated ID in `ids` is changed once, and every position that names it gets that result.

`mail.spam` follows `mail_spam` (default `allow`, sealed/error default `deny`) per message, returning `pending` with an `approval_id` or `refused by policy: mail_spam` for held or denied IDs. Each approval covers one message only. It never gets an action check. Microsoft 365 uses `POST /me/messages/{id}/move` with `destinationId: "junkemail"`; Gmail uses `messages.modify` with `addLabelIds: ["SPAM"]` and `removeLabelIds: ["INBOX"]`, not the trash endpoint; the fake provider moves to `spam`.
The `account` field is optional, and `params` must be an object.
Unknown operations or fields, duplicate keys, malformed JSON, trailing JSON, null fields, invalid types, and oversized requests are refused.
There is no HTTP login, token, key, raw-message, classification, rules-mutation, or approval endpoint. Only the [approval console](approval-console.md#approval-console), on its own address, approves or denies pending actions.
A pending action returns HTTP 200 with `{"status":"pending","approval_id":"...","url":"..."}`.
`approvals.status` takes `{"id":"APPROVAL_ID"}` (32 lowercase hex characters) and ignores `account`. An unknown ID returns HTTP 404 with `{"error":"approval not found"}`.
A policy refusal returns HTTP 500 with `{"error":"refused by policy: KEY"}`.

Requests are limited to 1 MiB and 16 JSON nesting levels.
The client bounds responses at 64 MiB and never follows redirects or retries requests, including stale-connection write replay.
Mail list and search take an optional `count` of 0 or more; 0 or no count sends no size, so the provider's default applies. They and `calendar.search` take an optional `page`: the `next_page` of an earlier result, at most 16 KiB of printable ASCII without spaces. A `next_page` from the provider that breaks these bounds fails the call. On Gmail the page is the provider's `pageToken`; a Gmail `calendar.search` page with more than the `events.list` default of 250 events fails. On Microsoft 365 a mail page is Graph's whole `@odata.nextLink`, which the daemon follows only with Graph's origin, the path of the same options, each query value of those options, and otherwise only `$top`, `$skip`, and `$skiptoken`; a calendar page is the Microsoft Search `from` offset.
Manual mutations need at least one ID or a `thread`, and recipient and attendee lists at least one address, except the `to`, `cc`, and `bcc` lists of `draft.new`, `mail.send`, `draft.forward`, `mail.forward`, and `draft.update`, and the `cc` and `bcc` lists of `draft.reply` and `mail.reply`, which can be empty (a `mail.send` or `mail.forward` needs one address among them); the daemon sets no maximum on these lists, because the provider refuses what it does not accept and the request size limit bounds them.
IDs and search strings are limited to 4,096 bytes.
A Gmail forward carries at most 100 attachments and inline images together, a bound of the daemon (Gmail documents no count limit), and at most 36,700,160 bytes of their data as encoded in the forward (base64, with a line break every 76 characters, about 4/3 of the raw size), Gmail's 35 MB limit for a raw message through the upload endpoints of `drafts.create` and `messages.send`. The daemon stops reading attachments as soon as the encoded total passes the limit. A forward over either bound fails with `Gmail error SizeError` before anything is written. A Gmail draft update reads the draft's raw message once (bounded like every Gmail response) and saves it through the upload endpoint of `drafts.update`; it has no attachment bounds of its own.
Stats accepts 1 to 365,000 days.

The daemon admits at most 64 concurrent calls.
The daemon sets no overall deadline that would cut off a long `mail organize` scan; set the client `--timeout` instead.
SIGINT, SIGTERM, or SIGHUP stops network admission, allows five seconds to drain, then cancels work with a ten-second shutdown limit.
The daemon logs no request arguments, bodies, credentials, or panic payloads.
There is no scheduler and no automatic mailbox work.
