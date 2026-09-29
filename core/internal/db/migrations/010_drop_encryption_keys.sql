-- Hard cutover (nothing in production): the local key backend no longer keeps
-- its AES key next to the ciphertext (audit I-6). Rows encrypted under a
-- DB-stored key can never be decrypted again, so they are removed; users
-- re-enter credentials on the Settings page.
DELETE FROM user_credentials;
DROP TABLE IF EXISTS encryption_keys;
