ALTER TABLE mail_cache ADD COLUMN tagged_verdict TEXT CHECK (tagged_verdict IN ('yellow', 'sealed', 'red'));
