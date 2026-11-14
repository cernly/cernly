DROP TABLE pets IF EXISTS;

CREATE TABLE pets (
  id         INTEGER IDENTITY PRIMARY KEY,
  name       VARCHAR(30),
  birth_date DATE
);
CREATE INDEX pets_name ON pets (name);
