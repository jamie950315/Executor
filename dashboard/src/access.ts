import { createRemoteJWKSet, jwtVerify } from "jose";

export interface AccessIdentity {
  subject: string;
}

export async function verifyAccess(request: Request, env: Env): Promise<AccessIdentity | null> {
  const assertion = request.headers.get("cf-access-jwt-assertion");
  if (assertion === null || assertion.length === 0 || assertion.length > 32 * 1024) {
    return null;
  }
  const teamDomain = env.ACCESS_TEAM_DOMAIN;
  const audience = env.ACCESS_AUD;
  if (!validTeamDomain(teamDomain) || audience.length === 0 || audience.length > 512) {
    return null;
  }
  const issuer = `https://${teamDomain}`;
  try {
    const keySet = createRemoteJWKSet(new URL(`${issuer}/cdn-cgi/access/certs`));
    const { payload } = await jwtVerify(assertion, keySet, {
      algorithms: ["RS256"],
      audience,
      issuer,
    });
    if (
      typeof payload.sub !== "string" ||
      payload.sub.trim().length === 0 ||
      payload.sub.length > 512 ||
      typeof payload.exp !== "number" ||
      !Number.isSafeInteger(payload.exp) ||
      (payload.nbf !== undefined && (!Number.isSafeInteger(payload.nbf) || payload.nbf > Date.now() / 1000))
    ) {
      return null;
    }
    return { subject: payload.sub };
  } catch {
    return null;
  }
}

function validTeamDomain(value: string): boolean {
  return (
    value.length > 0 &&
    value.length <= 253 &&
    !value.includes("/") &&
    !value.includes(":") &&
    /^[A-Za-z0-9.-]+$/u.test(value)
  );
}
