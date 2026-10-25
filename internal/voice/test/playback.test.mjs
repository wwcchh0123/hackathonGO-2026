import test from 'node:test';
import assert from 'node:assert/strict';
import { playbackAt } from '../ui/playback.mjs';
const frame = (start, text, value = .1) => ({start, end:start+.06, rate:16000, samples:new Float32Array(960).fill(value), text});
test('mouth follows playback position and sentence, not packet arrival', () => {
 const frames = [frame(1,'第一句'),frame(1.06,'第二句')];
 assert.equal(playbackAt(frames,.5,false).state,'buffering');
 assert.equal(playbackAt(frames,1.02,false).text,'第一句');
 assert.equal(playbackAt(frames,1.08,false).text,'第二句');
 assert.ok(playbackAt(frames,1.08,false).level > .5);
});
test('cloud stop does not stop queued audio; silence closes the mouth', () => {
 const frames=[frame(1,'还在播放'),frame(1.1,'静音',0)];
 assert.equal(playbackAt(frames,1.02,true).state,'playing');
 assert.equal(playbackAt(frames,1.08,true).state,'buffering');
 assert.equal(playbackAt(frames,1.12,true).level,0);
 assert.equal(playbackAt(frames,1.2,true).state,'idle');
 assert.equal(playbackAt(frames,1.2,false).state,'buffering');
});
test('interruption clears both queued mouth movement and text', () => {
 assert.deepEqual(playbackAt([],2,true),{state:'idle',level:0,text:''});
 assert.equal(playbackAt([frame(1,'响亮',1)],1.02,false).level,1);
});
