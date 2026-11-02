# Screening and privacy

What the daemon sends to the screening service, how to configure it, and what the agent sees for each verdict.

## Privacy and external screening

**Privacy warning:** Screening is an external service, not a local-only check.
By default, the daemon sends requests to OpenRouter's System One endpoint for model `typesafe/jev-1.13`.
The [`[screening]`](#screening) table can set another endpoint and model, which then receives the same data.
The fake screening engine sends nothing, but it is only for tests and local work.

- Mail requests include rendered body text, subject, sender and recipient identities (To, Cc, and the Bcc of drafts and sent mail where the provider gives it), attachment names, authentication results, and link domains.
- Calendar requests include subject, organizer, attendees, location, recurrence, meeting join URLs, and other event metadata.
- Private-event bodies are excluded, but private-event metadata and accepted meeting join URLs are still included.
- Organization requests include the rule instructions used for classification.
- [Action checks](write-policy.md#jev-action-check) include the recipients (To, Cc, and Bcc), subject, body (HTML as its source), HTML flag, reply-all flag, comment, and event fields that the agent wrote, for a reply the To and Cc addresses that the reply goes to, worked out from the original message, for a forward the subject and body of the forwarded message, and for a draft send the draft's To, Cc, Bcc, subject, body (HTML as its source), and HTML flag as the daemon reads them from the provider.

These transfers occur before the verdict.
Content hidden from the agent can still reach the screening service.
Meeting join URLs can contain access secrets.
Content-free local logs and encrypted credentials do not prevent external disclosure.

The project does not guarantee zero retention, no training, or a processing region.
Review [OpenRouter's provider policies](https://openrouter.ai/docs/guides/privacy/logging) and the applicable screening-service terms before using real mail.
Obtain any required organizational approval before connecting a work mailbox.

## Screening

The daemon screens every message and checks the text of agent actions with a decision engine. The optional `[screening]` table selects the engine. `amgd config set screening.provider fake` and the other `screening.*` keys change it without a hand edit:

```toml
[screening]
provider = "jev"                                     # Default. Use "fake" for the keyword engine.
endpoint = "https://openrouter.ai/api/v1/systemone"  # Default. Used only by "jev".
model = "typesafe/jev-1.13"                          # Default. Used only by "jev".
```

- `jev` calls Jev at `endpoint` with the stored screening key. The endpoint can be OpenRouter, Jev cloud, or another host that serves the same protocol. It must be an `https` URL with a host name, no user name or password, and no fragment, because each request carries mail text and the key. A port is optional and must be from 1 to 65535.
- `model` names the model that `jev` asks. The default, `typesafe/jev-1.13`, is the model name at OpenRouter. Jev cloud uses other model names. For example, to use Jev cloud:

  ```toml
  [screening]
  endpoint = "https://api.typesafe.ai/v1/systemone"
  model = "jev-1.13.0"
  ```

  A model name starts with a letter or a digit. It contains only letters, digits, `.`, `_`, `:`, `/`, and `-`, and it has a maximum of 128 characters. The screening thresholds are tuned for jev-1.13, so another model can change the verdicts.
- `fake` decides by keywords in the message or action. It makes no network calls and needs no key. It is for tests and local development:
  - "ignore previous instructions" makes a message `red`;
  - "verification code" makes a message `sealed`;
  - "SCREENFAIL" makes the screening fail, as when the engine is down;
  - "password" flags an action, so an allowed action waits for approval;
  - a folder or category rule is chosen when its rule name (the table name in `rules.toml`) is in the message.
- At start, `amgd serve` shows `screening key: not needed (fake screening)`. If a configured account is not a fake account, `amgd serve` also prints a warning: the mail of that account is then judged by keywords, not by a decision model.
- An absent table selects `jev` with the default endpoint and model. An unknown key or provider, an invalid endpoint, or an invalid model stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, for example `config.toml: screening.endpoint: must be an https URL`. A reload with such a change is refused. The error never shows the value.

## Verdicts

Each message gets a verdict. The verdict decides what the agent sees:

| Verdict | Visible mail content |
| --- | --- |
| green | Cleaned headers (sender, subject, To, Cc, and in `mail get` the Bcc of drafts and sent mail), body, attachment names, and link IDs |
| yellow | Cleaned headers (sender, subject, To, Cc, and in `mail get` the Bcc of drafts and sent mail), attachment names, and reason, without body |
| red or sealed | Sender address, date, and reason, without display name or subject |
| error | Date and safe error code, without sender, subject, or body |

Every verdict also includes the message ID, the thread ID, verdict, authentication results, read state, flag, mailbox-listed categories, and folders.
The thread ID (`thread_id` in `mail list`, `mail search`, `mail get`, and `mail thread` results) is the provider's conversation ID: the Gmail `threadId` or the Microsoft 365 `conversationId`. Messages with the same `thread_id` belong to one conversation. It is an opaque provider ID, not message text. It is left out when the provider gives none, so an empty ID never groups unrelated messages.
`folders` lists the folder IDs (Microsoft 365 and fake) or label IDs (Gmail) that the message is in. They are provider IDs, not message text.
Manual mutations work at every verdict, but the [write policy](write-policy.md#write-policy) can refuse or hold `mail trash`, also by verdict.
Automatic business actions remain green-only.
`mail organize --apply` can add existing `Yellow`, `Red`, or `Sealed` security tags to matching hidden mail.
The program never creates tags or changes their colors.
Successful action and security-tag markers prevent repeated organizer writes.
Ordinary reads do not modify the mailbox.
Cached hidden reads avoid fetching the body, cached green reads avoid Jev, and error verdicts retry screening on the next read.
All email-derived text remains untrusted, including green output.
HTML bodies become Markdown before screening.
The renderer drops non-rendered elements, comments, invisible Unicode, elements with the `hidden` attribute, and elements an inline style hides with `display:none`, `visibility:hidden` or `collapse`, `mso-hide:all`, `opacity` below 0.1, or a font size of 0 or below 2px or 2pt.
Where an inline hide removed prose, the Markdown keeps a `[hidden text removed]` marker so Jev and the agent see that the sender hid text; non-rendered markup and empty hidden boxes such as tracking pixels leave no marker.
It does not evaluate stylesheets, `!important` precedence, colour contrast, geometry, or clipping; that text stays in the Markdown so Jev and the agent see it.
