import { CookieJar } from "tough-cookie";
import { describe, expect, it } from "vitest";

import { browserIdentity, deviceGrantCookieName, deviceGrantSetCookie, readBoundedJSON } from "../../src/http";

describe("browser identity cookie lifetime", () => {
  it("persists for the same 30-day window as device grants", () => {
    const identity = browserIdentity(new Request("https://dashboard.example/api/session"));

    expect(identity.setCookie).toMatch(
      /^__Host-executor-browser=[A-Za-z0-9_-]{43}; Path=\/; Max-Age=2592000; Secure; HttpOnly; SameSite=Strict$/,
    );
  });
});

describe("device grant cookie scope", () => {
  it("covers exact delete and child routes without matching a sibling device", async () => {
    const jar = new CookieJar();
    const deviceID = "device-vector-1";
    const cookieName = await deviceGrantCookieName(deviceID);
    const cookiePair = `${cookieName}=signed.grant.value`;
    const setCookie = await deviceGrantSetCookie(deviceID, "signed.grant.value");
    await jar.setCookie(setCookie, `https://dashboard.example/api/devices/${deviceID}/unlock`);

    await expect(
      jar.getCookieString(`https://dashboard.example/api/devices/${deviceID}`),
    ).resolves.toBe(cookiePair);
    await expect(
      jar.getCookieString(`https://dashboard.example/api/devices/${deviceID}/call`),
    ).resolves.toBe(cookiePair);
    await expect(
      jar.getCookieString("https://dashboard.example/api/devices/device-vector-10/call"),
    ).resolves.toBe("");
  });
});

describe("bounded JSON requests", () => {
  it("keeps the request-too-large result when stream cancellation also fails", async () => {
    let cancelled = false;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([1, 2]));
      },
      cancel() {
        cancelled = true;
        throw new Error("transport cancellation failed");
      },
    });
    const request = new Request("https://dashboard.example/api/device/enroll", {
      method: "POST",
      body: stream,
      duplex: "half",
    } as RequestInit & { duplex: "half" });

    await expect(readBoundedJSON(request, 1)).rejects.toThrow("request too large");
    expect(cancelled).toBe(true);
  });
});
