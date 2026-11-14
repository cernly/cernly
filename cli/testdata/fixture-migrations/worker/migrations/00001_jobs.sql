-- +goose Up
-- +goose StatementBegin
CREATE TABLE jobs (
    id    bigint PRIMARY KEY,
    state text NOT NULL,
    note  text DEFAULT E'it\'s; fine'
);
CREATE FUNCTION touch() RETURNS trigger AS $$
BEGIN
  CREATE TABLE never (x int);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
DROP TABLE jobs;
