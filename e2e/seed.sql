CREATE TABLE users (
    id      SERIAL PRIMARY KEY,
    name    TEXT NOT NULL,
    email   TEXT NOT NULL UNIQUE,
    created TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    id         SERIAL PRIMARY KEY,
    user_id    INT NOT NULL REFERENCES users(id),
    amount     NUMERIC(10, 2) NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',
    created    TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO users (name, email) VALUES
    ('Alice',   'alice@example.com'),
    ('Bob',     'bob@example.com'),
    ('Charlie', 'charlie@example.com');

INSERT INTO orders (user_id, amount, status) VALUES
    (1, 99.99,  'completed'),
    (1, 14.50,  'pending'),
    (2, 250.00, 'completed'),
    (3, 7.99,   'cancelled');
