CREATE TABLE devices (
  device_id TEXT PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  platform TEXT NOT NULL,
  arch TEXT NOT NULL,
  version TEXT NOT NULL,
  mcp_url TEXT NOT NULL,
  public_jwk TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK (generation > 0),
  state TEXT NOT NULL CHECK (state IN ('offline', 'online')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_seen_at INTEGER
);

CREATE INDEX devices_state_idx ON devices(state);

CREATE TABLE audits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id TEXT,
  access_subject TEXT,
  action TEXT NOT NULL,
  outcome TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX audits_device_time_idx ON audits(device_id, created_at DESC);
