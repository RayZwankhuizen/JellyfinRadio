class RadioPCMCapture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.phase = 0;
    this.samples = [];
    this.sum = 0;
    this.count = 0;
  }

  process(inputs, outputs) {
    const channels = inputs[0] || [];
    if (!channels.length) return true;
    const input = channels[0];
    const second = channels[1];
    const ratio = 16000 / sampleRate;
    for (let i = 0; i < input.length; i++) {
      const sample = second ? (input[i] + second[i]) * 0.5 : input[i];
      this.sum += sample;
      this.count++;
      this.phase += ratio;
      if (this.phase >= 1) {
        this.phase -= 1;
        this.samples.push(this.sum / this.count);
        this.sum = 0;
        this.count = 0;
        if (this.samples.length === 15600) {
          const frame = Float32Array.from(this.samples);
          this.samples.length = 0;
          this.port.postMessage(frame.buffer, [frame.buffer]);
        }
      }
    }
    const output = outputs[0]?.[0];
    if (output) output.fill(0);
    return true;
  }
}

registerProcessor('radio-pcm-capture', RadioPCMCapture);
