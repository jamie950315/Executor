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
  it("does not delete a device refreshed after the delete snapshot was verified", async () => {
    const input = {
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
