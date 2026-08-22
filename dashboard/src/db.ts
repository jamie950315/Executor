import type { PublicKeyJWK } from "./shared/wire";

export interface DeviceRecord {
  device_id: string;
  name: string;
  platform: string;
  arch: string;
  version: string;
  mcp_url: string;
  public_jwk: PublicKeyJWK;
  generation: number;
  state: "offline" | "online";
  created_at: number;
  updated_at: number;
  last_seen_at: number | null;
}

export interface EnrollmentInput {
  device_id: string;
  name: string;
  platform: string;
  arch: string;
  version: string;
  mcp_url: string;
  public_jwk: PublicKeyJWK;
  generation: number;
}

interface StoredDevice extends Omit<DeviceRecord, "public_jwk"> {
  public_jwk: string;
}

const deviceColumns =
  "device_id, name, platform, arch, version, mcp_url, public_jwk, generation, state, created_at, updated_at, last_seen_at";

export async function enrollDevice(
  db: D1Database,
  input: EnrollmentInput,
  now: number,
): Promise<{ device: DeviceRecord; created: boolean } | null> {
  const publicJWK = canonicalPublicJWK(input.public_jwk);
  const existing = await db
    .prepare("SELECT public_jwk FROM devices WHERE device_id = ?")
    .bind(input.device_id)
    .first<{ public_jwk: string }>();
  if (existing !== null && existing.public_jwk !== publicJWK) {
    return null;
  }

  const stored = await db
    .prepare(
      `INSERT INTO devices (
        device_id, name, platform, arch, version, mcp_url, public_jwk, generation,
        state, created_at, updated_at, last_seen_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'offline', ?, ?, NULL)
      ON CONFLICT(device_id) DO UPDATE SET
        name = excluded.name,
        platform = excluded.platform,
        arch = excluded.arch,
        version = excluded.version,
        mcp_url = excluded.mcp_url,
        generation = MAX(devices.generation, excluded.generation),
        updated_at = excluded.updated_at
      WHERE devices.public_jwk = excluded.public_jwk
      RETURNING ${deviceColumns}`,
    )
    .bind(
      input.device_id,
      input.name,
      input.platform,
      input.arch,
      input.version,
      input.mcp_url,
      publicJWK,
      input.generation,
      now,
      now,
    )
    .first<StoredDevice>();
  if (stored === null) {
    return null;
  }
  return { device: decodeDevice(stored), created: existing === null };
}

export async function listDevices(db: D1Database): Promise<DeviceRecord[]> {
  const result = await db.prepare(`SELECT ${deviceColumns} FROM devices ORDER BY name, device_id`).all<StoredDevice>();
  return result.results.map(decodeDevice);
}

export async function getDevice(db: D1Database, deviceID: string): Promise<DeviceRecord | null> {
  const stored = await db
    .prepare(`SELECT ${deviceColumns} FROM devices WHERE device_id = ?`)
    .bind(deviceID)
    .first<StoredDevice>();
  return stored === null ? null : decodeDevice(stored);
}

export async function writeAudit(
  db: D1Database,
  deviceID: string | null,
  accessSubject: string | null,
  action: string,
  outcome: string,
  now: number,
): Promise<void> {
  await db
    .prepare(
      "INSERT INTO audits (device_id, access_subject, action, outcome, created_at) VALUES (?, ?, ?, ?, ?)",
    )
    .bind(deviceID, accessSubject, action, outcome, now)
    .run();
}

function decodeDevice(stored: StoredDevice): DeviceRecord {
  const parsed: unknown = JSON.parse(stored.public_jwk);
  if (!isPublicJWK(parsed)) {
    throw new Error("invalid device registry key");
  }
  return { ...stored, public_jwk: parsed };
}

function canonicalPublicJWK(jwk: PublicKeyJWK): string {
  return JSON.stringify({ kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y });
}

function isPublicJWK(value: unknown): value is PublicKeyJWK {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const record = value as Record<string, unknown>;
  return (
    Object.keys(record).length === 4 &&
    record.kty === "EC" &&
    record.crv === "P-256" &&
    typeof record.x === "string" &&
    typeof record.y === "string"
  );
}
