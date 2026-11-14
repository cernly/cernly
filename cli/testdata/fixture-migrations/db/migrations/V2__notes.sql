CREATE TABLE notes (id bigserial, account_id uuid REFERENCES accounts (id), body text);
ALTER TABLE notes ADD CONSTRAINT notes_pkey PRIMARY KEY (id);
