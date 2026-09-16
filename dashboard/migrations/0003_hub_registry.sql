CREATE TABLE hubs (
  hub_id TEXT PRIMARY KEY NOT NULL,
  token_hash TEXT UNIQUE NOT NULL CHECK (length(token_hash) = 64),
  public_jwk TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1))
);

CREATE TABLE hub_devices (
  hub_id TEXT NOT NULL REFERENCES hubs(hub_id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
  device_generation INTEGER NOT NULL CHECK (device_generation > 0),
  delegation_version INTEGER NOT NULL CHECK (delegation_version > 0),
  expires_at INTEGER NOT NULL,
  grant TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (hub_id, device_id)
);
