CREATE TABLE device_tombstones (
  device_id TEXT PRIMARY KEY NOT NULL,
  public_jwk TEXT NOT NULL,
  revoked_generation INTEGER NOT NULL CHECK (revoked_generation > 0),
  deleted_at INTEGER NOT NULL
);
