import { PCMFramer } from './pcm.mjs';

class Microphone extends AudioWorkletProcessor {
  constructor() {
    super();
    this.active = false;
    this.framer = new PCMFramer(sampleRate, frame => this.port.postMessage({ type: 'pcm', frame }, [frame]));
    this.port.onmessage = ({ data }) => {
      if (data === 'start') { this.framer.reset(); this.active = true; }
      if (data === 'stop') {
        this.active = false;
        this.framer.flush();
        this.port.postMessage({ type: 'stopped' });
      }
    };
  }
  process(inputs) {
    if (this.active && inputs[0]?.[0]) this.framer.push(inputs[0][0]);
    return true; // Output is silence; microphone audio is never monitored locally.
  }
}
registerProcessor('linx-microphone', Microphone);
