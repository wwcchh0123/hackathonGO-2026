(() => {
  'use strict';
  const html = JSON.parse(document.getElementById('linxi-document').textContent);
  const channel = new URL(location.href).searchParams.get('_linxi_channel');
  let started = false;
  function start(entries, persistent) {
    if (started) return;
    started = true;
    window.removeEventListener('message', initialize);
    if (persistent) {
      const values = new Map(entries);
      const snapshot = () => [...values.entries()];
      const publish = () => parent.postMessage({ type: 'linxi-storage-save', channel, entries: snapshot() }, '*');
      const set = (key, value) => {
        key = String(key); value = String(value);
        const candidate = new Map(values); candidate.set(key, value);
        if (candidate.size > 1000 || new TextEncoder().encode(JSON.stringify([...candidate])).length > 1024 * 1024) {
          throw new DOMException('作品本地数据超出存储上限', 'QuotaExceededError');
        }
        values.set(key, value); publish();
      };
      const methods = {
        getItem: (key) => values.get(String(key)) ?? null,
        setItem: set,
        removeItem: (key) => { values.delete(String(key)); publish(); },
        clear: () => { values.clear(); publish(); },
        key: (index) => [...values.keys()][Number(index)] ?? null,
      };
      const storage = new Proxy(Object.create(null), {
        get: (_, property) => property === 'length' ? values.size : Object.hasOwn(methods, property) ? methods[property] : values.get(property),
        set: (_, property, value) => { set(property, value); return true; },
        deleteProperty: (_, property) => { methods.removeItem(property); return true; },
        ownKeys: () => [...values.keys()],
        getOwnPropertyDescriptor: (_, property) => values.has(property) ? { configurable: true, enumerable: true, writable: true, value: values.get(property) } : undefined,
      });
      Object.defineProperty(window, 'localStorage', { value: storage, configurable: false });
    }
    document.open(); document.write(html); document.close();
  }
  function initialize(event) {
    const data = event.data;
    if (event.source !== parent || event.origin !== new URL(location.href).origin || data?.type !== 'linxi-storage-init' || data.channel !== channel) return;
    start(data.entries, data.persistent);
  }
  if (parent !== window && channel) {
    window.addEventListener('message', initialize);
    parent.postMessage({ type: 'linxi-storage-ready', channel }, '*');
    setTimeout(() => start([], false), 3000);
  } else {
    start([], false);
  }
})();
