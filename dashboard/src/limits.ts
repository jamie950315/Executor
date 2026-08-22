export const MAXIMUM_RELAY_MESSAGE_BYTES = 16 * 1024 * 1024;
export const MAXIMUM_RELAY_RESPONSE_BYTES = 64 * 1024 * 1024;

export function boundedResponseTotal(currentBytes: number, messageBytes: number): number | null {
  if (
    !Number.isSafeInteger(currentBytes) ||
    !Number.isSafeInteger(messageBytes) ||
    currentBytes < 0 ||
    messageBytes < 0 ||
    messageBytes > MAXIMUM_RELAY_MESSAGE_BYTES ||
    currentBytes > MAXIMUM_RELAY_RESPONSE_BYTES - messageBytes
  ) {
    return null;
  }
  return currentBytes + messageBytes;
}
