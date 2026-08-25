-- +goose Up
CREATE TABLE greetings (
    lang     TEXT PRIMARY KEY,
    greeting TEXT NOT NULL
);
INSERT INTO greetings (lang, greeting) VALUES ('en', 'hello');

-- +goose Down
DROP TABLE greetings;
