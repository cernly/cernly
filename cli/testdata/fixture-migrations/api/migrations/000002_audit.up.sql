CREATE TABLE audit.events (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, payload jsonb);
CREATE TABLE scratch (x int);
DROP TABLE scratch;
