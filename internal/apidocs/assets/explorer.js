'use strict';
const el = id => document.getElementById(id);
let selected, controller;
const maxBytes = 1024 * 1024;
fetch('/openapi/v1.json').then(r => r.json()).then(spec => {
  const operations = Object.entries(spec.paths).sort(([a], [b]) => a.localeCompare(b)).flatMap(([path, methods]) => Object.entries(methods).map(([method, operation]) => ({path, method, operation})));
  const render = () => {
    const needle = el('filter').value.toLowerCase();
    el('routes').replaceChildren();
    let group = '';
    for (const item of operations.filter(item => `${item.path} ${item.method}`.includes(needle))) {
      const section = item.path.split('/')[1] || '/';
      if (section !== group) {
        group = section;
        const heading = document.createElement('div');
        heading.className = 'group'; heading.textContent = `/${section}`;
        el('routes').append(heading);
      }
      const button = document.createElement('button');
      button.type = 'button'; button.setAttribute('aria-label', `${item.method.toUpperCase()} ${item.path}`);
      const method = document.createElement('span');
      method.className = `method m-${item.method}`; method.textContent = item.method.toUpperCase();
      const path = document.createElement('span'); path.textContent = item.path;
      button.append(method, path);
      button.onclick = () => {
        selected = item;
        for (const b of el('routes').querySelectorAll('button')) b.removeAttribute('aria-current');
        button.setAttribute('aria-current', 'true');
        el('title').textContent = `${item.method.toUpperCase()} ${item.path}`; el('description').textContent = item.operation.description;
        el('url').value = item.path; el('schema').textContent = JSON.stringify(item.operation, null, 2);
        el('confirm').checked = false; el('send').disabled = Boolean(controller);
        const examples = {'/v1/chat/completions': {model:'your-alias',messages:[{role:'user',content:'Hello'}]}, '/v1/embeddings': {model:'your-alias',input:'Hello'}, '/v1/responses': {model:'your-alias',input:'Hello',store:false}};
        el('body').value = JSON.stringify(examples[item.path] || {}, null, 2);
      };
      el('routes').append(button);
    }
  };
  el('filter').oninput = render; render();
}).catch(() => { el('status').textContent = 'Could not load the API specification.'; });
el('cancel').onclick = () => controller?.abort();
el('request').onsubmit = async event => {
  event.preventDefault();
  if (!selected || controller) return;
  const {method, operation} = selected;
  const readOnly = method === 'get' || method === 'head';
  el('output').textContent = '';
  let timeout;
  try {
    if (!readOnly && !el('confirm').checked) throw Error('Confirm the possible data change or provider usage first.');
    if (operation.requestBody?.content?.['multipart/form-data']) throw Error('Use an SDK or CLI for file uploads; the schema describes the fields.');
    const path = el('url').value;
    if (!path.startsWith('/') || path.startsWith('//') || path.includes('{') || path.includes('\\')) throw Error('Enter a same-origin path and replace all path parameters.');
    const target = new URL(path, location.origin);
    if (target.origin !== location.origin || !/^\/(v1|admin|me|auth|invite|reset|healthz|readyz)(\/|$)/.test(target.pathname)) throw Error('Only same-origin gateway API paths are allowed.');
    let body;
    if (!readOnly) { JSON.parse(el('body').value); body = el('body').value; }
    const headers = {'Content-Type':'application/json'};
    if (el('key').value) headers.Authorization = `Bearer ${el('key').value}`;
    controller = new AbortController(); el('send').disabled = true; el('cancel').disabled = false;
    timeout = setTimeout(() => controller?.abort(), 90000);
    el('status').textContent = 'Waiting for response…';
    const response = await fetch(target, {method:method.toUpperCase(),headers,body,signal:controller.signal,credentials:'same-origin',redirect:'error'});
    el('status').textContent = `${response.status} ${response.statusText}`;
    const reader = response.body.getReader(), decoder = new TextDecoder();
    let bytes = 0;
    for (;;) {
      const {value,done} = await reader.read();
      if (done) break;
      bytes += value.byteLength;
      if (bytes > maxBytes) { await reader.cancel(); el('status').textContent += ' · Display stopped at 1 MiB'; break; }
      el('output').append(document.createTextNode(decoder.decode(value,{stream:true})));
    }
    el('output').append(document.createTextNode(decoder.decode()));
  } catch (error) { el('status').textContent = error.name === 'AbortError' ? 'Request cancelled' : error.message; }
  finally { clearTimeout(timeout); controller = undefined; el('send').disabled = !selected; el('cancel').disabled = true; }
};
