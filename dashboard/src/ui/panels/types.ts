import type { CallResult } from "../api";

export type DeviceCall = (
  method: string,
  argumentsValue: unknown,
  signal?: AbortSignal,
) => Promise<CallResult>;

export interface PanelProps {
  call: DeviceCall;
  active?: boolean;
}
