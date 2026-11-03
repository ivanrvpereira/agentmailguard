# Verification and limits

Tests run offline: real loopback HTTP, temporary homes, the fake provider, fake Graph, Gmail, and Jev endpoints, and a fake Keychain `Platform`.
No test needs real mailbox data, OpenRouter, or Keychain access.
Only the optional console browser tests need a browser.
CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs `make`, `go fix -diff`, `staticcheck`, `govulncheck`, and a Gitleaks scan of the whole history on Linux, and `make` on macOS, which builds and tests the macOS-only Keychain backend (`internal/credentials/keychain_darwin.go`).

The end-to-end tests in `internal/e2e` build the real `amg` and `amgd` binaries and run `amgd serve` as its own process, on a temporary home with a fake account, the fake screening engine, and the file credential backend. The other `amgd` commands and `amg` run as separate processes, and the tests approve and deny pending actions through the approval console over HTTP.
After each test, the daemon log, `audit.jsonl`, and every column of `cache.db` except the `args` and `draft` columns of `pending` must contain no seeded name, subject, body, or address.

The approval console tests in `internal/server` drive the console handler over HTTP, on a real daemon with a fake Graph endpoint and a fake Jev endpoint.
They check the pages, redirects, headers, cookies, audit records, and the writes that reach the fake provider.
The console browser tests (`TestBrowserConsole...` in `internal/server/console_browser_test.go`) run the same handler in headless Chrome through chromedp.
They run only with `AMG_BROWSER_TESTS=1`, and they skip when no Chrome or Chromium is installed, so `make` skips them.
CI runs them in a separate job on `ubuntu-24.04`, which fails if Chrome is missing.
They cover the console script: Auto-refresh pauses while a box is ticked or a row, Recents, or the menu is open, and reloads after the idle time with the filters only.
They also cover the header checkbox (checked and partly checked), Approve that waits for `I reviewed it`, and the waiting count in the tab title.
They check that the Rendered preview runs no script of the body, loads nothing remote, and that a click on a link or a submit button in it goes nowhere, that an HTML write from a script fails, and that no page logs another policy violation.
They check at phone, tablet, and desktop widths that the rows keep room for the event and the subject, and that the page does not scroll sideways.
Only a manual check in a browser covers the rest of the console layout, dark mode, and the pages with the script off.

Not verified against real providers or services:

- mail sends, replies, and forwards: Cc, Bcc, and `--html` bodies on new drafts and sends were checked live on 2026-10-26 on Microsoft 365 and Gmail (received copies show no Bcc, sent copies show it, and `mail get` shows the Bcc of drafts and sent mail, from Graph `bccRecipients` and the Gmail `Bcc` header); the other cases, such as no Bcc read of received mail (Gmail without the `SENT` label, Microsoft 365 `from` another address) and Cc, Bcc, and `--html` added to replies and forwards, are verified only against fake Graph and Gmail endpoints;
- forwards: Graph `createForward` with no recipients and an empty body, and the Gmail inline forward with its attachments and inline images, which follows Google's unsupported `gws` CLI, were checked live on 2026-10-26; the other cases are verified only against fake Graph and Gmail endpoints: the patch of the note into Graph's quoted forward and of its recipients, and the Gmail header block byte for byte;
- a Gmail forward of a message with an attached message (`message/*`) for which Gmail gives neither inline data nor an `attachmentId` fails with `Gmail error ValueError`: the daemon never sends such an attachment empty;
- a forward on Microsoft 365 that fails after `createForward` leaves its forward draft in Drafts;
- draft updates: the Microsoft 365 `PATCH` of the given fields, and the Gmail `drafts.update` rebuild from the raw message with a base64 body part that keeps the thread, the inline images, and the attachments, were checked live on 2026-10-26; the other cases are verified only against fake Graph and Gmail endpoints: kept headers byte for byte (folded `References`, `From`), dropped `Date` and `Message-ID`, attached messages and other kept parts, the body added to an attachment-only draft, and the refusals of a message that is not a draft;
- draft delete and draft send: the refusals of a message that is not a draft and the single write without retry are verified only against fake Graph and Gmail endpoints and the fake provider (checked live on 2026-10-26: Graph `DELETE` moves a draft out of Drafts into a folder that is neither Deleted Items nor in `mail folders`, likely Recoverable Items, where it stays readable by ID and so restorable; Graph `send` of a forward draft sends it and moves it to Sent Items; Gmail `drafts.delete` removes the draft, and later reads fail; Gmail `drafts.send` of an HTML forward draft with inline images sends it and removes the draft);
- waiting sends and draft changes: the replacement of a waiting send after a Gmail draft update that changed the message ID was checked live on 2026-10-26 (the new waiting send under the new ID was approved and sent); the other cases are verified end to end with the fake provider, which keeps the draft ID on update, and in the guard tests, and an approval that comes while a draft update holds the write lock in the server tests;
- reply recipients: Microsoft's `createReplyAll` list matched the reply rule, the patch of the agent's body went above Graph's quoted reply, and the Gmail reply-all rule, which follows Google's unsupported `gws` CLI and the Gmail web behavior that it approximates (no Gmail API page states it), held also for a reply to the account's own sent message; these were checked live on 2026-10-26. The other cases, such as Microsoft's `createReply` list and the patch of Cc and Bcc into Microsoft's lists, are verified only against fake Graph and Gmail endpoints;
- a reply on Microsoft 365 that fails after `createReply` leaves its reply draft in Drafts;
- meeting invites, updates, cancellations, and responses: `--html` event bodies on `calendar invite` and `calendar update` were checked live on 2026-10-26 on Microsoft 365 and Gmail; the other cases are verified only against fake Graph and Google Calendar endpoints and the fake provider;
- provider acceptance of generated drafts;
- conversation actions: checked live on 2026-10-31 on Microsoft 365 and Gmail, each action from the Inbox and with `--in` a user folder or label, Sent, and the trash (the Gmail move from a label, the skip of trashed messages on Gmail, the archive from the trash (to Archive on Microsoft 365, no change on Gmail), and the well-known names `archive`, `sentitems`, and `deleteditems` on Microsoft 365); a move with `--in` the trash checked live on 2026-11-01 (Gmail removes `TRASH` and adds the new label; Microsoft 365 moves the message from Deleted Items to the new folder); not checked live: a move from a Microsoft 365 folder below Deleted Items, spam or junk as `--in`, and conversations with more than a few messages;
- `mail thread` on a Microsoft 365 conversation with a message in Junk Email (checked live on 2026-10-26: Gmail threads with drafts, and Microsoft 365 conversations across Inbox, Sent Items, Drafts, Deleted Items, and other folders; `calendar search` on both providers was checked the same day);
- Microsoft 365 device-code login, which organization policies can also refuse;
- paging with `--page` and `next_page`, and the default page sizes, on `mail list`, `mail search`, and `calendar search`;
- `mail search` date bounds: the Gmail `after:` and `before:` query in Unix seconds one second wider on each side, and the Microsoft 365 KQL `received` days one day wider on each side, each with exact times filtered by the daemon;
- requests to Jev cloud, or to any screening endpoint or model other than the default;
- migration of a real Keychain record.

Offline verification does not establish production readiness.
