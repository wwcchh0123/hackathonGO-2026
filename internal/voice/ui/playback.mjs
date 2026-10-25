// Read the scheduled audio timeline, including silence and buffering gaps.
// A small look-ahead compensates for the screen's AirPlay video latency.
export function playbackAt(frames, time, stopped) {
  const frame = frames.find(f => time >= f.start && time < f.end);
  if (!frame) return {state: stopped && !frames.some(f => f.end > time) ? 'idle' : 'buffering', level: 0, text: ''};
  const start = Math.max(0, Math.floor((time - frame.start) * frame.rate));
  const end = Math.min(frame.samples.length, start + Math.ceil(frame.rate * .025));
  let power = 0;
  for (let i = start; i < end; i++) power += frame.samples[i] ** 2;
  const rms = Math.sqrt(power / Math.max(1, end - start));
  return {state: 'playing', level: Math.min(1, Math.max(0, (rms - .008) * 7)), text: frame.text};
}
