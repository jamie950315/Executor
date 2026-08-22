import { unlockDevice } from "./api";
import type { SessionContext } from "./api";
import { sealRecoveryEnvelope } from "../shared/crypto";
import type { PublicKeyJWK } from "../shared/wire";

type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export async function performUnlock(
  device: { device_id: string; public_jwk: PublicKeyJWK; generation: number },
  session: SessionContext,
  recoveryKey: string,
  signal?: AbortSignal,
  fetcher?: Fetcher,
): Promise<void> {
  if (recoveryKey.length === 0) throw new Error("Recovery key is required");
  const envelope = await sealRecoveryEnvelope(
    device.public_jwk,
    {
      device_id: device.device_id,
      access_subject: session.access_subject,
      browser_id: session.browser_id,
      generation: device.generation,
    },
    recoveryKey,
  );
  await unlockDevice(device.device_id, envelope, signal, fetcher);
}
