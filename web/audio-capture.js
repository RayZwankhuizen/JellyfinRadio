class RadioPCMCapture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.phase = 0;
    this.samples = [];
    this.sum = 0;
    this.count = 0;
    this.history = new Int16Array(16000 * 10);
    this.historyOffset = 0;
    this.historyLength = 0;
    this.recording = false;
    this.recorded = new Int16Array(15600);
    this.recordedLength = 0;
    this.port.onmessage = ({data}) => {
      if (data === 'start-recording' && !this.recording) {
        const start = (this.historyOffset - this.historyLength + this.history.length) % this.history.length;
        const beforeWrap = this.history.subarray(start, start + this.historyLength);
        const preRoll = new Int16Array(this.historyLength);
        preRoll.set(beforeWrap);
        preRoll.set(this.history.subarray(0, this.historyLength - beforeWrap.length), beforeWrap.length);
        this.sendRecordingChunk(preRoll);
        this.recording = true;
      } else if (data === 'stop-recording' && this.recording) {
        this.flushRecording();
        this.recording = false;
        this.port.postMessage({type: 'recording-complete'});
      }
    };
  }

  sendRecordingChunk(samples) {
    this.port.postMessage({type: 'recording-chunk', samples: samples.buffer}, [samples.buffer]);
  }

  flushRecording() {
    if (!this.recordedLength) return;
    this.sendRecordingChunk(this.recorded.slice(0, this.recordedLength));
    this.recordedLength = 0;
  }

  captureSample(sample) {
    const pcm = Math.round(Math.max(-1, Math.min(1, sample)) * 32767);
    this.history[this.historyOffset] = pcm;
    this.historyOffset = (this.historyOffset + 1) % this.history.length;
    this.historyLength = Math.min(this.historyLength + 1, this.history.length);
    if (this.recording) {
      this.recorded[this.recordedLength++] = pcm;
      if (this.recordedLength === this.recorded.length) this.flushRecording();
    }
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
        const averaged = this.sum / this.count;
        this.samples.push(averaged);
        this.captureSample(averaged);
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
