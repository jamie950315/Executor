import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { HubManager } from "../../src/ui/HubManager";
import type { HubService } from "../../src/ui/hub-api";
import type { DeviceView } from "../../src/ui/DeviceGrid";

const device = {device_id:"mac",name:"Mac",generation:1,state:"online",uiState:"unlocked"} as DeviceView;
const publicKey = {kty:"EC",crv:"P-256",x:"a".repeat(43),y:"b".repeat(43)} as const;
function service() {
  return {list:vi.fn().mockResolvedValue([{hub_id:"pi5",public_key:publicKey,enabled:true}]),links:vi.fn().mockResolvedValue([]),register:vi.fn(),disable:vi.fn(),delegate:vi.fn().mockResolvedValue(undefined),revoke:vi.fn()} satisfies HubService;
}

it("requires explicit confirmation and an unlocked, unchanged device", async () => {
  const api = service();
  const {rerender}=render(<HubManager devices={[device]} onUnlock={vi.fn()} onNotice={vi.fn()} api={api}/>);
  await screen.findByRole("button",{name:"Authorize Mac"});
  await waitFor(()=>expect(screen.getByRole("button",{name:"Authorize Mac"})).toBeEnabled());
  fireEvent.click(screen.getByRole("button",{name:"Authorize Mac"}));
  expect(api.delegate).not.toHaveBeenCalled();
  rerender(<HubManager devices={[{...device,generation:2}]} onUnlock={vi.fn()} onNotice={vi.fn()} api={api}/>);
  expect(screen.getByRole("button",{name:"Confirm authorization"})).toBeDisabled();
  fireEvent.click(screen.getByRole("button",{name:"Cancel"}));
  fireEvent.click(screen.getByRole("button",{name:"Authorize Mac"}));
  fireEvent.click(screen.getByRole("button",{name:"Confirm authorization"}));
  await waitFor(()=>expect(api.delegate).toHaveBeenCalledTimes(1));
  expect(api.delegate).toHaveBeenCalledWith("mac","pi5");
});

it("does not transmit private key registration data", async () => {
  const api=service();render(<HubManager devices={[]} onUnlock={vi.fn()} onNotice={vi.fn()} api={api}/>);
  fireEvent.change(screen.getByLabelText("Public registration JSON"),{target:{value:JSON.stringify({hub_id:"pi5",public_key:{...publicKey,d:"private-marker"},token_hash:"a".repeat(64)})}});
  fireEvent.click(screen.getByRole("button",{name:"Review registration"}));
  expect(await screen.findByRole("alert")).toHaveTextContent("public");
  expect(api.register).not.toHaveBeenCalled();
});

it("reports uncertain mutations without retrying", async () => {
  const api=service();api.delegate.mockRejectedValue(new Error("unconfirmed"));
  render(<HubManager devices={[device]} onUnlock={vi.fn()} onNotice={vi.fn()} api={api}/>);
  await waitFor(()=>expect(screen.getByRole("button",{name:"Authorize Mac"})).toBeEnabled());
  fireEvent.click(screen.getByRole("button",{name:"Authorize Mac"}));
  fireEvent.click(screen.getByRole("button",{name:"Confirm authorization"}));
  expect(await screen.findByRole("alert")).toHaveTextContent("Nothing was retried");
  expect(api.delegate).toHaveBeenCalledTimes(1);
});
