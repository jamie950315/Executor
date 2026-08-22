import type { DeviceRecord, SessionContext } from "./api";
import { generateSensitiveResponseKey, openSensitiveResultEnvelope, type SensitiveResult, type SensitiveResultContext } from "./sensitive-result";
import type { DeviceCall } from "./panels/types";

export async function runSensitiveLifecycle(
  method: "control.rotate" | "control.kill",
  device: DeviceRecord,
  session: SessionContext,
  call: DeviceCall,
): Promise<SensitiveResult> {
  const responseKey = await generateSensitiveResponseKey();
  const response = await call(method, { response_public_key: responseKey.publicJWK }, new AbortController().signal);
  const result = record(response.result);
  const envelope = result?.sensitive_result;
  const expected: SensitiveResultContext = {
    device_id: device.device_id,
    access_subject: session.access_subject,
    browser_id: session.browser_id,
    generation: device.generation + 1,
    request_id: response.requestID,
    method,
  };
  return openSensitiveResultEnvelope(responseKey.privateKey, envelope, expected);
}

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null;
}
