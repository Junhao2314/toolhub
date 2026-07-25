ALTER TABLE users ADD COLUMN username text;

DO $$
DECLARE
    item record;
    base_username text;
    candidate text;
    suffix integer;
    suffix_text text;
BEGIN
    FOR item IN SELECT id, email FROM users ORDER BY lower(email), id LOOP
        base_username := regexp_replace(lower(btrim(split_part(item.email, '@', 1))), '[^a-z0-9._-]+', '-', 'g');
        base_username := btrim(base_username, '-');
        IF char_length(base_username) < 3 THEN
            base_username := 'user-' || substr(md5(lower(item.email) || item.id::text), 1, 8);
        END IF;
        base_username := left(base_username, 32);
        candidate := base_username;
        suffix := 2;
        WHILE EXISTS (SELECT 1 FROM users WHERE username = candidate) LOOP
            suffix_text := suffix::text;
            candidate := left(base_username, 32 - char_length(suffix_text)) || suffix_text;
            suffix := suffix + 1;
        END LOOP;
        UPDATE users SET username = candidate WHERE id = item.id;
    END LOOP;
END $$;

UPDATE users SET email = lower(btrim(email));

ALTER TABLE users
    ALTER COLUMN username SET NOT NULL,
    ADD COLUMN password_change_recommended boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT users_username_format CHECK (
        username = lower(username)
        AND char_length(username) BETWEEN 3 AND 32
        AND username ~ '^[a-z0-9._-]+$'
        AND position('@' in username) = 0
    );

CREATE UNIQUE INDEX users_username_lower_idx ON users (lower(username));
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));
