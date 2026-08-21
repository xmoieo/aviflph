// worker.js — Web Worker for aviflph WASM

importScripts("wasm_exec.js");

let wasmReady = false;

async function initWasm() {
  if (wasmReady) return;
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(fetch("aviflph.wasm"), go.importObject);
  go.run(result.instance);
  wasmReady = true;
}

function postResult(id, result) { postMessage({ id, ok: true, result }); }
function postError(id, msg) { postMessage({ id, ok: false, error: msg }); }

function checkError() {
  const err = self.__lp_error;
  if (err) { self.__lp_error = null; throw new Error(err); }
}

self.onmessage = async function (e) {
  const { id, op, data, opts } = e.data;

  try {
    await initWasm(); checkError();
    switch (op) {
      case "init": postResult(id, "ok"); break;
      case "getmeta": postResult(id, self.lp_getmeta(data)); break;
      case "demux": postResult(id, self.lp_demux(data)); break;
      case "extract_still": postResult(id, self.lp_extract_still(data)); break;
      case "extract_video": postResult(id, self.lp_extract_video(data)); break;
      case "embed": postResult(id, self.lp_embed(data.still, data.video)); break;
      case "encode_still": postResult(id, self.lp_encode_still(data, opts.quality, opts.speed)); break;
      default: postError(id, "Unknown: " + op);
    }
  } catch (e) { postError(id, e.message || String(e)); }
};
