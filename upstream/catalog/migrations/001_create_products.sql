-- +goose Up
CREATE TABLE products (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    price_cents INT NOT NULL CHECK (price_cents >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO products (name, description, price_cents) VALUES
    ('Mechanical Keyboard', 'Cherry MX Brown switches, TKL layout', 12999),
    ('USB-C Hub', '7-in-1: HDMI, USB-A x3, SD, microSD, PD 100W', 4999),
    ('Monitor Arm', 'Gas spring, 17-32 inch, VESA 75/100', 3499);

-- +goose Down
DROP TABLE products;
