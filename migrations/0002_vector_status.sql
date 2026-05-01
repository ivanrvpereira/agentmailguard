ALTER TABLE emails ADD COLUMN vector_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE emails ADD COLUMN vector_error TEXT;
CREATE INDEX idx_emails_vector_status ON emails(vector_status);
