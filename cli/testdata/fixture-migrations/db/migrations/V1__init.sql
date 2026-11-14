-- Flyway: versions compare as numbers (V2 before V10).
CREATE TYPE account_state AS ENUM ('active', 'closed');

CREATE TABLE accounts (
    id         uuid PRIMARY KEY,
    email      varchar(320) NOT NULL UNIQUE,
    state      account_state NOT NULL DEFAULT 'active',
    created_at timestamp with time zone NOT NULL DEFAULT now()
);
