import test from 'node:test';
import assert from 'node:assert/strict';
import { PCMFramer } from '../ui/pcm.mjs';

for (const rate of [16000, 44100, 48000, 96000]) {
  test(`${rate} Hz keeps three seconds and is independent of callback boundaries`, () => {
    const input = Float32Array.from({ length: rate * 3 }, (_, i) => Math.sin(i / 15) * .4);
    const run = size => {
      const frames = []; const f = new PCMFramer(rate, b => frames.push(Buffer.from(b)));
      for (let i = 0; i < input.length; i += size) f.push(input.subarray(i, i + size));
      f.flush(); return frames;
    };
    const frames = run(128);
    assert.equal(frames.length, 50);
    assert.ok(frames.every(f => f.byteLength === 1920));
    assert.deepEqual(Buffer.concat(frames), Buffer.concat(run(37)));
  });
}
test('clamps PCM16, pads final frame and flushes exactly once', () => {
  const frames = []; const f = new PCMFramer(16000, b => frames.push(new DataView(b)));
  f.push(new Float32Array([2, -2, .5])); f.flush(); f.flush();
  assert.equal(frames.length, 1);
  assert.equal(frames[0].getInt16(0, true), 32767);
  assert.equal(frames[0].getInt16(2, true), -32768);
  assert.equal(frames[0].getInt16(4, true), 16384);
  assert.equal(frames[0].getInt16(6, true), 0);
});
