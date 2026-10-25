(() => {
  'use strict';
  const frame = document.getElementById('artifact');
  const error = document.getElementById('storage-error');
  const key = `linxi.preview.${document.body.dataset.storageScope}`;
  const channel = Array.from(crypto.getRandomValues(new Uint8Array(16)), (value) => value.toString(16).padStart(2, '0')).join('');
  const maxBytes = 1024 * 1024;
  const valid = (entries) => Array.isArray(entries) && entries.length <= 1000 &&
    entries.every((entry) => Array.isArray(entry) && entry.length === 2 && entry.every((value) => typeof value === 'string')) &&
    new TextEncoder().encode(JSON.stringify(entries)).length <= maxBytes;
  let persistent = true;
  let entries = [];
  const warn = () => { error.textContent = '作品的本地数据暂时无法保存，刷新后可能丢失。'; error.hidden = false; };
  try {
    const stored = localStorage.getItem(key);
    if (stored) {
      const value = JSON.parse(stored);
      if (!valid(value)) throw new Error('invalid stored data');
      entries = value;
    }
  } catch {
    persistent = false;
    warn();
  }
  window.addEventListener('message', (event) => {
    const data = event.data;
    if (event.source !== frame.contentWindow || event.origin !== 'null' || !data || data.channel !== channel) return;
    if (data.type === 'linxi-storage-ready') {
      frame.contentWindow.postMessage({ type: 'linxi-storage-init', channel, entries, persistent }, '*');
    } else if (data.type === 'linxi-storage-save' && valid(data.entries)) {
      try {
        localStorage.setItem(key, JSON.stringify(data.entries));
        entries = data.entries;
      } catch {
        warn();
      }
    }
  });
  const source = new URL(location.href);
  source.searchParams.set('_linxi_raw', '1');
  source.searchParams.set('_linxi_channel', channel);
  frame.src = source.href;
})();
