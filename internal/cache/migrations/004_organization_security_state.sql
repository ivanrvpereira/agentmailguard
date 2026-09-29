ALTER TABLE mail_cache RENAME COLUMN applied_at TO organization_applied_at;
ALTER TABLE mail_cache RENAME COLUMN tagged_verdict TO security_applied_verdict;
