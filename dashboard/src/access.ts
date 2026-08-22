import { createRemoteJWKSet, jwtVerify, type JWTVerifyGetKey } from "jose";

export interface AccessIdentity {
  subject: string;
}

interface AccessEnvironment {
  ACCESS_TEAM_DOMAIN: string;
  ACCESS_AUD: string;
}

type AccessJWKSetFactory = (url: URL) => JWTVerifyGetKey;

export function createAccessVerifier(
  createJWKSet: AccessJWKSetFactory = createRemoteJWKSet,
): (request: Request, env: AccessEnvironment) => Promise<AccessIdentity | null> {
  const resolvers = new Map<string, JWTVerifyGetKey>();
  return async (request, env) => {
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
    const certsURL = new URL(`${issuer}/cdn-cgi/access/certs`);
    try {
      let keySet = resolvers.get(certsURL.href);
      if (keySet === undefined) {
        keySet = createJWKSet(certsURL);
        resolvers.set(certsURL.href, keySet);
      }
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
        typeof payload.nbf !== "number" ||
        !Number.isSafeInteger(payload.nbf) ||
        payload.nbf > Date.now() / 1000
      ) {
        return null;
      }
      return { subject: payload.sub };
    } catch {
      return null;
    }
  };
}

export const verifyAccess = createAccessVerifier();

function validTeamDomain(value: string): boolean {
  return (
    value.length > 0 &&
    value.length <= 253 &&
    !value.includes("/") &&
    !value.includes(":") &&
    /^[A-Za-z0-9.-]+$/u.test(value)
  );
}
