# Meetings and availability

Meeting invitations, updates, cancellations, and responses, and the free/busy query.

## Meetings

**Caution:** If the write policy allows them, `calendar invite`, `calendar update`, `calendar cancel`, and `calendar respond` (except `respond --no-notify` on Microsoft 365) send real meeting notifications at once.
The [Jev action check](write-policy.md#jev-action-check) can hold a command for approval, but it is a statistical check: it can miss harmful text.
An agent must run these commands only after the user authorizes that specific action.

The [write policy](write-policy.md#write-policy) denies these commands by default.
To use them, set `calendar_invite`, `calendar_update`, `calendar_cancel`, and `calendar_respond` to `allow` in `config.toml`.
These commands work for Microsoft 365, Gmail, and fake accounts. On Gmail, they change the primary calendar.

Microsoft 365 login requests the `Calendars.ReadWrite` scope, and token refreshes request it too.
Without this grant, meeting commands fail with `missing_scopes` before any Graph calendar request.
After `missing_scopes`, run `amgd account login ALIAS` again on the daemon host.
Gmail login requests the `calendar.events.owned` scope. Without this grant, meeting commands fail with `missing_scopes` before any Google Calendar request.

Times use the format `YYYY-MM-DD HH:MM` in the local timezone of the client.
The client refuses other formats, times that do not exist because of a DST change, and years outside 1–9999.
The client does not refuse a time that occurs two times when clocks go back. It uses one of the two offsets, so do not use such a time.
`--duration` takes values such as `30m` or `1h30m` and must be more than zero.
An invite needs exactly one of `--end` and `--duration`. An update can use one of them, but not both.

An invite needs one or more `--to` addresses and an explicit `--subject`, which can be empty.
The body is empty if you omit `--body`.
`--body` is plain text unless `--html` is given.
The invite sends plain text as escaped HTML and keeps the line breaks, so the text cannot become markup or a link with hidden text.
With `--html`, `--body` is HTML that the daemon sends unchanged, without cleaning or checking it: as the Graph event body with content type `html` on Microsoft 365, and as the event description on Gmail.
Every invite has an online meeting link by default: a Microsoft Teams meeting on Microsoft 365, and a Google Meet link on Gmail.
For a Teams meeting, Microsoft adds the Teams join block to the body.
Use `--no-online` to create a meeting without an online meeting.
`--to` attendees are required attendees. `--optional ADDRESS` (repeatable) adds optional attendees: a Graph attendee of type `optional` on Microsoft 365, and a Google Calendar attendee with `"optional": true` on Gmail. Optional attendees also receive the invitation. An address in both `--to` and `--optional`, ignoring case, is an invalid request.
On Gmail, guests cannot invite other people to a meeting that the daemon creates or updates.

An update needs at least one change and sends only the options that you supply.
An explicit empty `--subject ''`, `--location ''`, or `--body ''` clears that field.
`--body` replaces the whole body. As in an invite, plain text goes as escaped HTML that keeps the line breaks, and an `--html` body goes unchanged. `--html` without `--body` is invalid usage. On Microsoft 365, `--body` replaces the whole description, including the Teams join section. On Gmail, the Meet link is not part of the body and stays.
If an update changes only `--start`, the daemon keeps the current duration.
`--duration` alone or `--end` alone keeps the current start.
Attendee changes keep the type and the response of existing attendees. Added attendees are required attendees.
`calendar update ID --online` adds an online meeting to the same event and sends an update notification to the attendees. It does not create a new event.
On Microsoft 365, it adds a Teams meeting, and Microsoft Graph can add Teams join details to the body.
On Gmail, it adds a Google Meet link only if the event has no conference. If the event already has one, the update keeps it.
With `--online` alone, the update sends only online-meeting settings. It does not replace the body or change the times or attendees.
An update without `--online` leaves the online meeting unchanged. Updates cannot remove an online meeting or change the online-meeting provider. Without `--body`, an existing Teams link stays in the body.
`--online=false` on an update is invalid usage.

Only the organizer can update or cancel an event.
Only an attendee can respond. The response goes to the organizer unless you use `--no-notify`.
On Microsoft 365, `calendar respond --no-notify` records your response without notifying the organizer (Graph `sendResponse` set to `false`). `--no-notify` with `--comment` is invalid usage, because the comment would reach nobody; on the HTTP interface it is `invalid request`.
Google Calendar always notifies the organizer of a response. On Gmail, `calendar respond --no-notify` fails with `calendar response refused: responses without notification are not supported for this account`, before any credential or provider access.
There is no proposal of a new time.
On Gmail, a response changes only your own attendee entry, and the event must list you as an attendee.
The daemon refuses recurring events and cancelled events. It does not write in these cases.

Every Gmail meeting command notifies all guests (`sendUpdates=all`).
On Gmail, `calendar cancel` deletes the event and notifies the guests. Google keeps the deleted event on your calendar with the status `cancelled`, so it can be restored from the calendar trash for 30 days. The daemon never deletes an event permanently.
Google sends no cancellation message. On Gmail, `calendar cancel --comment` fails with `calendar cancel refused: comments are not supported for this account`, before any credential or provider access. On Microsoft 365, the comment goes with the cancellation.

Invites need at least one required attendee address. The daemon sets no maximum on attendee lists: the provider refuses what it does not accept.

The client does not retry a meeting request, and the daemon sends no transaction ID.
A meeting command can return `operation failed` after the notification was sent, for example if the audit write fails.
A timeout, transport error, or `operation failed` does not prove that the meeting action failed.
Before you retry such a command, use `calendar list` or `calendar get` to verify the outcome.
A second invite can send duplicate invitations.

## Calendar availability

`calendar availability` shows the free/busy status of participants in 30-minute slots.
It is read-only. It sends no notification, reserves no slot, and writes no audit record.
It works only for Microsoft 365 and [fake](fake-provider.md#fake-provider) accounts. Gmail accounts get the fixed error `calendar availability is not supported for this account`, before any credential or provider access.
The `Calendars.ReadBasic`, `Calendars.Read`, or `Calendars.ReadWrite` grant is sufficient. Without one of these grants, the command fails with `missing_scopes` before any Graph request. Fake accounts need no grant.

```sh
amg --account work calendar availability --to me@example.com --to ana@example.com --start '2026-10-09 09:00' --end '2026-10-09 18:00'
```

The command needs 1–20 distinct `--to` addresses, a `--start`, and an `--end`.
The window must be less than 62 days. These are Microsoft Graph limits for one query.
Times use the same local format as the meeting commands. The interval is always 30 minutes.

The result has `start`, `end`, `interval_minutes`, and one entry in `schedules` for each requested address, in the request order.
Each entry has the requested `address` and a `slots` list.
Slot `i` starts at `start` plus `30 * i` minutes. The last slot ends at `end`, so it can be shorter than 30 minutes.
Each slot is `free`, `tentative`, `busy`, `out_of_office`, or `unknown`.
Microsoft Graph reports "working elsewhere" as `free`.
The result never contains event subjects, locations, or provider error text.

`unknown` means that no usable data came back for that participant.
Sharing permissions in the Microsoft 365 organization can cause this result. A missing, duplicate, failed, or malformed entry also gives `unknown`.
`unknown` never means free.
A slot is free for a meeting only if every requested participant has `free` in that slot.

The result is a snapshot. Calendars can change after the query.
Before you send an invite, run the query again for the selected slot.
`calendar list` shows only the visible events of the selected account. It does not show that other participants are free.
