import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../beta_body_probe.js', import.meta.url), 'utf8');
const directory = '/private/tmp/executor-beta-final-fixture';
const id = '11111111-1111-4111-8111-111111111111';
function setup(body, responseId = id) {
  let requests = 0;
  const native = async () => { requests++; return new Response(body, {status:502,headers:{'content-type':'application/json; charset=utf-8','x-executor-request-id':responseId}}); };
  const window = {fetch:native};
  const context = vm.createContext({window,location:{origin:'https://beta-executor-dashboard.0ruka.dev'},URL,
    AbortController,AbortSignal,TextDecoder,Uint8Array,Set,setTimeout,clearTimeout,atob});
  vm.runInContext(source, context);
  const probe = context.installBetaBodyProbe(directory);
  return {window,probe,native,requests:()=>requests};
}
async function run(value) {
  const response = await value.window.fetch('/api/devices/device-f8WsfPqGN5f8vbVTSFGQS49l4eXn8lqV/call',{
    method:'POST',credentials:'same-origin',body:JSON.stringify({method:'filesystem_read',arguments:{action:'read_file',path:directory+'/wait.fifo'}})
  });
  await response.body.cancel(); // Existing UI behavior; independent clone must reach EOF.
  const deadline = Date.now() + 1000;
  while (!value.probe.snapshot().results['wait.fifo'] && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 1));
  }
  return value.probe.snapshot().results['wait.fifo'];
}
test('one response clone completes JSON/EOF despite UI cancellation, without another request',async()=>{
  const body=JSON.stringify({code:'relay_stream_failed',request_id:id,outcome:'unconfirmed'});
  const value=setup(body);
  const result=await run(value);
  assert.equal(result.accepted,true);
  assert.equal(result.body_bytes,Buffer.byteLength(body));
  assert.equal(result.normal_eof,true);
  assert.equal(result.read_exception,false);
  assert.equal(value.requests(),1);
  assert.equal(value.probe.snapshot().counts['wait.fifo'],1);
  assert.equal(value.probe.cleanup().pending_at_cleanup,0);
  assert.equal(value.window.fetch,value.native);
});
test('mismatching body/header identity is rejected',async()=>{
  const value=setup(JSON.stringify({code:'relay_stream_failed',request_id:'wrong',outcome:'unconfirmed'}));
  assert.equal((await run(value)).accepted,false);
  value.probe.cleanup();
});
test('empty and malformed JSON are not reported as accepted',async()=>{
  for(const body of ['', '{']) {
    const value=setup(body);
    const result=await run(value);
    assert.equal(result.accepted,false);
    assert.equal(result.utf8_json_valid,undefined);
    value.probe.cleanup();
  }
});
