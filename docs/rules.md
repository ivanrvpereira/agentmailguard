# Rules

`mail organize` sorts mail by meaning: a rule names a target folder or category and describes in plain words which messages belong there. Jev, the decision engine, picks the rule for each `green` message. Run it without `--apply` first to see the plan.

Example `rules.toml` in the daemon home:

```toml
[accounts."me@example.com".receipts]
folder = "archive"
min_age_hours = 24
instructions = "Receipts that need no further action."

[accounts."me@example.com".finance]
category = "Finance"
instructions = "Messages about invoices or receipts."
```

Each table under `accounts` is named by the email of an account, in quotes, for example `[accounts."me@example.com".receipts]`. Emails compare in lowercase.
Rules follow the mailbox, not the alias, so a renamed alias keeps its rules.
The daemon ignores the rules for an email that no account has. `amgd config check` warns about them: `rules.toml has rules for EMAIL, which is no account; the daemon ignores them`.
The daemon reads `rules.toml` only at start and at a reload. After you edit it, run `amgd reload` on the daemon host.
`amgd` never writes `rules.toml`.

A folder target is a mailbox folder ID, `archive`, or `trash`.
On every provider, a folder ID in the trash or spam/junk, including their subfolders, is a closed destination and fails. Use `trash` for the dedicated trash action under `mail_trash`; no spam action is available. Rule actions also refuse a source that is now in a closed folder before policy, approval, or a provider write.
A category target must already exist in the mailbox.
`rules list` shows resolved names and missing-target warnings, never instructions or confidence scores.
Missing rules produce an empty organizer report without provider or Jev access.
Invalid rules stop `amgd serve`, and a reload with invalid rules is refused with `rules_invalid`.
`stats` needs neither account configuration nor provider authentication.
`amgd cache clear` runs only on the daemon host. It removes the verdicts, saved organization answers, and completion markers of all accounts, but not pending actions. The API has no cache operation.

`mail organize --since` and `--until` follow the date rule in [Commands and output](commands.md#commands-and-output): days on the CLI machine, `--until` day included.
The calendar range has no maximum: Microsoft Graph `calendarView` needs a start and an end but sets no maximum range.
