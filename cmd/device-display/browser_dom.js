// Fixed device-side DOM adapter, never supplied by an MCP caller. Each CDP
// target owns its node references, including accessible same-process frames.
// Opaque/sandboxed frames are inspected separately through their CDP targets;
// no sandbox or same-origin protection on the preview is removed.
({action, start = 0, limit = 240, elementID}) => {
  const key = '__linxiBrowserNodes';
  const compact = (value, size) => String(value || '').replace(/\s+/g, ' ').trim().slice(0, size);
  const disabled = node => node.matches(':disabled') || node.getAttribute('aria-disabled') === 'true';
  const rectInView = node => {
    const win = node.ownerDocument.defaultView;
    const style = win.getComputedStyle(node);
    if (style.visibility !== 'visible' || style.display === 'none' || Number(style.opacity) === 0) return null;
    const r = node.getBoundingClientRect();
    let left = Math.max(0, r.left), top = Math.max(0, r.top);
    let right = Math.min(win.innerWidth, r.right), bottom = Math.min(win.innerHeight, r.bottom);
    for (let parent = node.parentElement; parent; parent = parent.parentElement) {
      const s = win.getComputedStyle(parent), p = parent.getBoundingClientRect();
      if (/(auto|scroll|hidden|clip)/.test(s.overflowX)) { left = Math.max(left, p.left); right = Math.min(right, p.right); }
      if (/(auto|scroll|hidden|clip)/.test(s.overflowY)) { top = Math.max(top, p.top); bottom = Math.min(bottom, p.bottom); }
    }
    return right > left && bottom > top ? {x: left, y: top, width: right-left, height: bottom-top} : null;
  };
  // Convert a same-process child's point through its frame elements. For an
  // OOPIF, window is already the target viewport: CDP routes mouse input there.
  const pointInTarget = (node, checkHit = true) => {
    const r = rectInView(node);
    if (!r) return null;
    let x = r.x + r.width / 2, y = r.y + r.height / 2, win = node.ownerDocument.defaultView;
    const hit = node.ownerDocument.elementFromPoint(x, y);
    if (checkHit && hit !== node && !node.contains(hit)) return null;
    while (win !== window) {
      const frame = win.frameElement;
      if (!frame || !rectInView(frame)) return null;
      const fr = frame.getBoundingClientRect();
      const sx = fr.width / frame.offsetWidth, sy = fr.height / frame.offsetHeight;
      x = fr.x + (frame.clientLeft + x) * sx;
      y = fr.y + (frame.clientTop + y) * sy;
      win = frame.ownerDocument.defaultView;
      if (checkHit && frame.ownerDocument.elementFromPoint(x, y) !== frame) return null;
    }
    return {x, y};
  };
  if (action === 'click') {
    const ref = window[key]?.get(elementID);
    const node = ref?.node;
    if (!node?.isConnected || node.ownerDocument.URL !== ref.url) return {found: false};
    if (disabled(node)) return {found: true, error: '元素已禁用'};
    node.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
    const point = pointInTarget(node);
    return point ? {found: true, ...point} : {found: true, error: '元素被遮挡或不可见，请重新查看页面'};
  }
  const refs = new Map();
  window[key] = refs; // Old IDs and detached nodes are never recycled.
  const result = {url: location.href, title: document.title, visible_text: '', frames: [], elements: [], partial: false};
  const escapeHTML = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
  // Return rendered DOM, not a guessed list of buttons. divs with delegated
  // event handlers, cards, SVG and canvas keep their structure and references.
  // Input values and executable script/style bodies are not part of this view.
  const attributes = ['id', 'class', 'role', 'title', 'aria-label', 'aria-labelledby', 'aria-expanded', 'aria-pressed', 'aria-selected', 'aria-disabled', 'tabindex', 'type', 'name', 'placeholder', 'alt', 'data-action', 'data-testid'];
  const seen = new Set();
  const visit = doc => {
    if (seen.has(doc)) return;
    if (seen.size >= 8) { result.partial = true; return; }
    seen.add(doc);
    const texts = [];
    const walker = doc.createTreeWalker(doc.body || doc.documentElement, NodeFilter.SHOW_TEXT);
    let n, count = 0, textSize = 0;
    while ((n = walker.nextNode()) && ++count < 6000 && textSize < 2400) {
      const text = compact(n.textContent, 300), parent = n.parentElement;
      if (!text || !parent || parent.closest('script,style,noscript')) continue;
      if (!rectInView(parent) || !pointInTarget(parent, false)) continue;
      texts.push(text); textSize += text.length;
    }
    const frame = {url: doc.URL, title: doc.title, visible_text: texts.join(' ').slice(0, 2400), html: ''};
    result.frames.push(frame);
    if (doc === document) result.visible_text = frame.visible_text;
    let visits = 0;
    const render = (node, depth) => {
      if (node.nodeType === Node.TEXT_NODE) return escapeHTML(compact(node.textContent, 160));
      if (node.nodeType !== Node.ELEMENT_NODE) return '';
      if (++visits > 6000 || depth > 30) { result.partial = true; return ''; }
      if (node.matches('script,style,noscript,link,meta,input[type=hidden]') || !rectInView(node)) return '';
      if (result.elements.length >= limit) { result.partial = true; return '<!-- snapshot limit -->'; }
      const id = 'e' + (start + result.elements.length + 1);
      refs.set(id, {node, url: doc.URL});
      const r = rectInView(node);
      const labelled = (node.getAttribute('aria-labelledby') || '').split(/\s+/).map(id => doc.getElementById(id)?.textContent || '').join(' ');
      const label = node.getAttribute('aria-label') || labelled.trim() || node.getAttribute('title') || node.getAttribute('placeholder');
      const tag = node.tagName.toLowerCase();
      const directText = Array.from(node.childNodes).filter(n => n.nodeType === Node.TEXT_NODE).map(n => n.textContent).join(' ');
      const entry = {element_id: id, tag, role: node.getAttribute('role') || '', label: compact(label, 120), text: node.matches('input,textarea') ? '' : compact(directText, 160), frame_url: doc.URL, disabled: disabled(node), bounds: r};
      if (node.hasAttribute('aria-expanded')) entry.expanded = node.getAttribute('aria-expanded') === 'true';
      else if (tag === 'summary') entry.expanded = node.parentElement.open;
      result.elements.push(entry);
      const attrs = attributes.filter(a => node.hasAttribute(a)).map(a => ' ' + a + '="' + escapeHTML(compact(node.getAttribute(a), 160)) + '"').join('');
      let children = '';
      if (!node.matches('input,textarea,select')) {
        for (const child of node.childNodes) {
          if (result.elements.length >= limit) { result.partial = true; break; }
          children += render(child, depth + 1);
        }
      }
      return '<' + tag + ' data-element-id="' + id + '"' + attrs + '>' + children + '</' + tag + '>';
    };
    frame.html = render(doc.body || doc.documentElement, 0);
    for (const frameElement of doc.querySelectorAll('iframe,frame')) {
      if (!pointInTarget(frameElement)) continue;
      // Cross-origin and sandboxed documents have a separate target; attempting
      // contentDocument here doesn't bypass their isolation.
      try { if (frameElement.contentDocument) visit(frameElement.contentDocument); } catch {}
    }
  };
  visit(document);
  return result;
}
