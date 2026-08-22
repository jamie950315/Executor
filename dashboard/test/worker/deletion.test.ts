import { applyD1Migrations, env } from "cloudflare:test";
import { beforeEach, describe, expect, inject, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { deleteDeviceConditionally, enrollDevice, getDevice } from "../../src/db";

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.batch([
    env.DB.prepare("DELETE FROM audits"),
    env.DB.prepare("DELETE FROM devices"),
    env.DB.prepare("DELETE FROM device_tombstones"),
  ]);
});

describe("atomic device deletion", () => {
  it("deletes the verified identity after a liveness-only heartbeat update", async () => {
    const input = deviceInput();
    await enrollDevice(env.DB, input, 1_700_000_000_000);
    const deleteSnapshot = await getDevice(env.DB, input.device_id);
    if (deleteSnapshot === null) {
      throw new Error("missing delete snapshot");
    }

    await env.DB.prepare(
      `UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ?
      WHERE device_id = ? AND generation = ?`,
    )
      .bind(1_700_000_000_001, 1_700_000_000_001, input.device_id, input.generation)
      .run();

    await expect(
      deleteDeviceConditionally(env.DB, deleteSnapshot, 1_700_000_000_002),
    ).resolves.toEqual({ deleted: true });
    await expect(getDevice(env.DB, input.device_id)).resolves.toBeNull();
    await expect(
      env.DB.prepare(
        "SELECT revoked_generation FROM device_tombstones WHERE device_id = ?",
      )
        .bind(input.device_id)
        .first<{ revoked_generation: number }>(),
    ).resolves.toEqual({ revoked_generation: 8 });
  });

  it("does not delete a higher-generation re-enrollment after the delete snapshot", async () => {
    const input = deviceInput();
    await enrollDevice(env.DB, input, 1_700_000_000_000);
    const deleteSnapshot = await getDevice(env.DB, input.device_id);
    if (deleteSnapshot === null) {
      throw new Error("missing delete snapshot");
    }

    await enrollDevice(env.DB, { ...input, name: "Concurrent Refresh", generation: 8 }, 1_700_000_000_001);
    const result = await deleteDeviceConditionally(env.DB, deleteSnapshot, 1_700_000_000_002);

    expect(result).toEqual({ deleted: false });
    await expect(getDevice(env.DB, input.device_id)).resolves.toMatchObject({
      name: "Concurrent Refresh",
      generation: 8,
    });
  });
});

function deviceInput() {
  return {
    device_id: "device-vector-1",
    name: "Before Refresh",
    platform: "darwin",
    arch: "arm64",
    version: "0.1.0",
    mcp_url: "https://device.example/mcp",
    public_jwk: {
      kty: "EC" as const,
      crv: "P-256" as const,
      x: vectors.device_public_key.x,
      y: vectors.device_public_key.y,
    },
    generation: 7,
  };
}
