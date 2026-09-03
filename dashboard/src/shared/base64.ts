export function encodeBase64URL(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

export function decodeBase64URL(value: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]+$/u.test(value)) {
    throw new Error("invalid base64url");
  }
  const remainder = value.length % 4;
  if (remainder === 1) {
    throw new Error("invalid base64url");
  }
  try {
    const padded = value.replaceAll("-", "+").replaceAll("_", "/") + "=".repeat((4 - remainder) % 4);
    const binary = atob(padded);
    const decoded = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    if (encodeBase64URL(decoded) !== value) {
      throw new Error("invalid base64url");
    }
    return decoded;
  } catch {
    throw new Error("invalid base64url");
  }
}
