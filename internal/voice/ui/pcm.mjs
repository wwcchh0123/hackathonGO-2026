// Streaming, weighted-area resampler. Fractional state survives audio callbacks,
// including 44.1 kHz devices, so frames never drift or drop callback tails.
export class PCMFramer {
  constructor(inputRate, emit, frameSize = 960) {
    if (inputRate < 16000) throw new Error('Microphone sample rate must be at least 16 kHz');
    this.ratio = inputRate / 16000;
    this.emit = emit;
    this.frameSize = frameSize;
    this.reset();
  }
  reset() {
    this.remaining = this.ratio;
    this.sum = 0;
    this.frame = new ArrayBuffer(this.frameSize * 2);
    this.view = new DataView(this.frame);
    this.index = 0;
  }
  sample(value) {
    const clamped = Math.max(-1, Math.min(1, value));
    this.view.setInt16(this.index++ * 2, Math.round(clamped * (clamped < 0 ? 32768 : 32767)), true);
    if (this.index === this.frameSize) {
      this.emit(this.frame);
      this.frame = new ArrayBuffer(this.frameSize * 2);
      this.view = new DataView(this.frame);
      this.index = 0;
    }
  }
  push(samples) {
    for (const value of samples) {
      let left = 1;
      while (left > 1e-8) {
        const take = Math.min(left, this.remaining);
        this.sum += value * take;
        this.remaining -= take;
        left -= take;
        if (this.remaining < 1e-8) {
          this.sample(this.sum / this.ratio);
          this.remaining = this.ratio;
          this.sum = 0;
        }
      }
    }
  }
  flush() {
    if (this.remaining < this.ratio - 1e-8) this.sample(this.sum / this.ratio);
    if (this.index) this.emit(this.frame); // Unused tail is zero-filled.
    this.reset();
  }
}
