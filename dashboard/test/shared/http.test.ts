import { CookieJar } from "tough-cookie";
import { describe, expect, it } from "vitest";

import { deviceGrantCookieName, deviceGrantSetCookie } from "../../src/http";

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
