-- Whether a device's proxies, when open to its network, ask for a user and
-- password. The password itself is derived from the device's secret.
ALTER TABLE devices ADD COLUMN proxy_auth INTEGER NOT NULL DEFAULT 0;
