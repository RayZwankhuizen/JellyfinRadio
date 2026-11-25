const vadAssets = 'https://cdn.jsdelivr.net/npm/@ricky0123/vad-web@0.0.31/dist/';
const ortAssets = 'https://cdn.jsdelivr.net/npm/onnxruntime-web@1.22.0/dist/';
let library;

function loadScript(url) {
  return new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = url;
    script.crossOrigin = 'anonymous';
    script.onload = resolve;
    script.onerror = () => {
      script.remove();
      reject(new Error('Silero-bestanden konden niet worden geladen.'));
    };
    document.head.append(script);
  });
}

async function loadLibrary() {
  if (!library) {
    library = (async () => {
      if (!window.ort) await loadScript(ortAssets + 'ort.wasm.min.js');
      await loadScript(vadAssets + 'bundle.min.js');
      return window.vad.MicVAD;
    })().catch(error => {
      library = undefined;
      throw error;
    });
  }
  return library;
}

export async function startSilero({context, source, signal, onStart, onEnd}) {
  const MicVAD = await loadLibrary();
  signal.throwIfAborted();
  const input = context.createMediaStreamDestination();
  source.connect(input);
  let detector;
  const dispose = async () => {
    source.disconnect(input);
    for (const track of input.stream.getTracks()) track.stop();
    await detector?.destroy();
  };
  try {
    detector = await MicVAD.new({
      model: 'v5',
      audioContext: context,
      baseAssetPath: vadAssets,
      onnxWASMBasePath: ortAssets,
      ortConfig: ort => { ort.env.wasm.numThreads = 1; },
      getStream: async () => input.stream,
      onSpeechRealStart: onStart,
      onSpeechEnd: onEnd,
    });
    signal.throwIfAborted();
    return dispose;
  } catch (error) {
    await dispose();
    throw error;
  }
}
