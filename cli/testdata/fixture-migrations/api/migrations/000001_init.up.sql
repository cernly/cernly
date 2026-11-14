CREATE TABLE IF NOT EXISTS sessions (
    token_sha256 bytea PRIMARY KEY,
    user_id      bigint NOT NULL,
    expires_at   timestamptz NOT NULL
);
