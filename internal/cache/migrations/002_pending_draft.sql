-- draft holds, for a waiting draft send, the draft fields that the Jev action check saw (to, cc, bcc, subject,
-- body, html), beside its args, which hold only the draft ID. NULL for every other action. It is mail content.
ALTER TABLE pending ADD COLUMN draft TEXT;
