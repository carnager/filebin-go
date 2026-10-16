(function() {
  'use strict';

  function $(s, c) { return (c || document).querySelector(s); }
  function $$(s, c) { return Array.prototype.slice.call((c || document).querySelectorAll(s)); }
  function store(k, v) {
    try {
      if (v === undefined) return localStorage.getItem(k);
      if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v);
    } catch (e) { return null; }
  }
  function el(tag, attrs, text) {
    var e = document.createElement(tag);
    for (var k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (text != null) e.textContent = text;
    return e;
  }
  function fmtBytes(b) {
    if (b < 1024) return b + ' B';
    var u = 'KMGTPE', i = -1;
    do { b /= 1024; i++; } while (b >= 1024 && i < 5);
    return b.toFixed(1) + ' ' + u[i] + 'iB';
  }

  var csrf = ($('meta[name=csrf-token]') || {}).content || '';

  function api(method, url, body) {
    return fetch(url, {
      method: method, credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: body ? JSON.stringify(body) : undefined
    }).then(function(r) {
      return r.json().catch(function() { return {}; }).then(function(j) {
        if (!r.ok) throw new Error(j.error || ('Request failed (' + r.status + ')'));
        return j;
      });
    });
  }

  function copyText(t) {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(t);
    return new Promise(function(res, rej) {
      var ta = el('textarea');
      ta.value = t;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy') ? res() : rej(new Error('copy failed')); } catch (e) { rej(e); }
      ta.remove();
    });
  }

  function feedback(btn, txt) {
    var s = $('span', btn);
    if (btn._fbT) clearTimeout(btn._fbT); else btn._fbOrig = s ? s.textContent : btn.getAttribute('title');
    if (s) s.textContent = txt; else btn.setAttribute('title', txt);
    btn.classList.add('ok');
    btn._fbT = setTimeout(function() {
      if (s) s.textContent = btn._fbOrig; else btn.setAttribute('title', btn._fbOrig);
      btn.classList.remove('ok');
      btn._fbT = null;
    }, 1500);
  }

  function toast(msg, isErr) {
    var t = el('div', { 'class': 'toast' + (isErr ? ' error' : ''), role: isErr ? 'alert' : 'status' }, msg);
    document.body.appendChild(t);
    requestAnimationFrame(function() { t.classList.add('show'); });
    setTimeout(function() { t.classList.remove('show'); setTimeout(function() { t.remove(); }, 300); }, 3000);
  }

  // Generic modal: {title, desc, body: Node, confirm, danger, onConfirm: fn -> Promise|false}
  function modal(o) {
    var ov = el('div', { 'class': 'modal-overlay' });
    var m = el('div', { 'class': 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'modal-h' });
    m.appendChild(el('h3', { id: 'modal-h' }, o.title));
    if (o.desc) m.appendChild(el('p', null, o.desc));
    if (o.body) m.appendChild(o.body);
    var err = el('div', { 'class': 'alert error', role: 'alert' });
    err.hidden = true;
    m.appendChild(err);
    var act = el('div', { 'class': 'modal-actions' });
    var cancel = el('button', { type: 'button', 'class': 'btn sm' }, 'Cancel');
    var ok = el('button', { type: 'button', 'class': 'btn sm ' + (o.danger ? 'danger' : 'primary') }, o.confirm || 'OK');
    act.appendChild(cancel);
    act.appendChild(ok);
    m.appendChild(act);
    ov.appendChild(m);
    document.body.appendChild(ov);
    var prev = document.activeElement;
    requestAnimationFrame(function() { ov.classList.add('open'); });
    function close() {
      ov.classList.remove('open');
      document.removeEventListener('keydown', key, true);
      setTimeout(function() { ov.remove(); }, 200);
      if (prev && prev.focus) prev.focus();
    }
    function key(e) {
      if (e.key === 'Escape') { e.preventDefault(); close(); }
      else if (e.key === 'Enter' && e.target.tagName === 'INPUT' && e.target.type !== 'checkbox') { e.preventDefault(); ok.click(); }
      else if (e.key === 'Tab') {
        var f = $$('button,input,select,a[href]', m).filter(function(x) { return !x.disabled && x.offsetParent !== null; });
        if (!f.length) return;
        if (e.shiftKey && document.activeElement === f[0]) { e.preventDefault(); f[f.length - 1].focus(); }
        else if (!e.shiftKey && document.activeElement === f[f.length - 1]) { e.preventDefault(); f[0].focus(); }
      }
    }
    document.addEventListener('keydown', key, true);
    ov.addEventListener('click', function(e) { if (e.target === ov) close(); });
    cancel.addEventListener('click', close);
    ok.addEventListener('click', function() {
      ok.disabled = true;
      err.hidden = true;
      Promise.resolve().then(o.onConfirm).then(function(r) {
        ok.disabled = false;
        if (r !== false) close();
      }, function(e) {
        ok.disabled = false;
        err.textContent = e.message || String(e);
        err.hidden = false;
      });
    });
    var first = $('input,select', m) || ok;
    setTimeout(function() { first.focus(); }, 30);
    return close;
  }

  function field(label, attrs) {
    var w = el('div', { 'class': 'field' });
    var id = 'f-' + Math.random().toString(36).slice(2);
    w.appendChild(el('label', { 'for': id }, label));
    attrs.id = id;
    attrs['class'] = 'input';
    var tag = attrs.tag || 'input';
    delete attrs.tag;
    var i = el(tag, attrs);
    w.appendChild(i);
    return { wrap: w, input: i };
  }

  function checkField(label) {
    var l = el('label', { 'class': 'check' });
    var c = el('input', { type: 'checkbox' });
    l.appendChild(c);
    l.appendChild(document.createTextNode(' ' + label));
    return { wrap: l, input: c };
  }

  // --- Theme: system -> light -> dark ---
  (function() {
    var btn = $('#theme-toggle');
    if (!btn) return;
    var modes = ['system', 'light', 'dark'];
    function cur() { var t = store('theme'); return t === 'light' || t === 'dark' ? t : 'system'; }
    function apply(m) {
      if (m === 'system') document.documentElement.removeAttribute('data-theme');
      else document.documentElement.setAttribute('data-theme', m);
      var nxt = modes[(modes.indexOf(m) + 1) % 3];
      btn.title = 'Theme: ' + m + ' (click for ' + nxt + ')';
      btn.setAttribute('aria-label', btn.title);
    }
    btn.addEventListener('click', function() {
      var m = modes[(modes.indexOf(cur()) + 1) % 3];
      store('theme', m === 'system' ? null : m);
      apply(m);
    });
    apply(cur());
  })();

  // --- Image fallbacks (thumb -> raw, or hide) ---
  function imgFail(t) {
    var fb = t.getAttribute('data-fallback');
    if (fb && t.getAttribute('src') !== fb) { t.src = fb; return; }
    if (t.classList.contains('row-thumb')) t.style.visibility = 'hidden';
  }
  document.addEventListener('error', function(e) {
    if (e.target && e.target.tagName === 'IMG') imgFail(e.target);
  }, true);
  $$('img[data-fallback],img.row-thumb').forEach(function(t) {
    if (t.complete && t.getAttribute('src') && !t.naturalWidth) imgFail(t);
  });

  // --- Copy buttons (anywhere) ---
  document.addEventListener('click', function(e) {
    var b = e.target.closest && e.target.closest('[data-copy]');
    if (!b) return;
    e.preventDefault();
    copyText(location.origin + b.getAttribute('data-copy')).then(function() {
      feedback(b, 'Copied');
    }, function() { toast('Could not copy to clipboard', true); });
  });

  // --- Share bar ---
  $$('.sharebar').forEach(function(bar) {
    var id = bar.getAttribute('data-id'), isCol = bar.hasAttribute('data-collection');
    var qrBtn = $('[data-qr]', bar);
    if (qrBtn) {
      var pop = qrBtn.nextElementSibling;
      var closePop = function() { pop.hidden = true; qrBtn.setAttribute('aria-expanded', 'false'); };
      qrBtn.addEventListener('click', function(e) {
        e.stopPropagation();
        var img = $('img', pop);
        if (!img.src) img.src = img.getAttribute('data-src');
        pop.hidden = !pop.hidden;
        qrBtn.setAttribute('aria-expanded', String(!pop.hidden));
      });
      document.addEventListener('click', function(e) { if (!pop.hidden && !pop.contains(e.target)) closePop(); });
      document.addEventListener('keydown', function(e) { if (e.key === 'Escape' && !pop.hidden) { closePop(); qrBtn.focus(); } });
    }

    var ren = $('[data-rename]', bar);
    var title = $('[data-title]');
    if (ren && title) {
      ren.addEventListener('click', function() {
        if ($('input', title.parentNode)) return;
        var orig = title.textContent, empty = title.getAttribute('data-empty');
        var inp = el('input', { type: 'text', 'class': 'input inline-edit', 'aria-label': isCol ? 'Collection name' : 'File name' });
        inp.value = empty && orig === empty ? '' : orig;
        title.hidden = true;
        title.parentNode.insertBefore(inp, title.nextSibling);
        inp.focus();
        inp.select();
        var done = false;
        function finish(save) {
          if (done) return;
          done = true;
          var v = inp.value.trim();
          inp.remove();
          title.hidden = false;
          if (!save || v === orig || (!v && !isCol)) return;
          api('PATCH', '/api/' + id, { name: v }).then(function() {
            title.textContent = v || empty || '';
            document.title = (v || empty) + ' — filebin';
            toast('Renamed');
          }, function(err) { toast(err.message, true); });
        }
        inp.addEventListener('keydown', function(e) {
          if (e.key === 'Enter') { e.preventDefault(); finish(true); }
          else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
        });
        inp.addEventListener('blur', function() { finish(true); });
      });
    }

    var del = $('[data-delete]', bar);
    if (del) {
      del.addEventListener('click', function() {
        var chk = isCol ? checkField('Also delete the files in this collection') : null;
        modal({
          title: isCol ? 'Delete this collection?' : 'Delete this file?',
          desc: 'This cannot be undone.', danger: true, confirm: 'Delete',
          body: chk ? chk.wrap : null,
          onConfirm: function() {
            return api('DELETE', '/api/' + id + (chk && chk.input.checked ? '?files=true' : '')).then(function() {
              location.href = '/history';
            });
          }
        });
      });
    }
  });

  // --- Collection member removal ---
  $$('[data-col-remove]').forEach(function(b) {
    b.addEventListener('click', function(e) {
      e.preventDefault();
      var fid = b.getAttribute('data-col-remove'), cid = b.getAttribute('data-col');
      api('PATCH', '/api/' + cid, { remove: [fid] }).then(function() {
        var item = b.closest('.thumb-wrap,.col-item');
        if (item) item.remove();
        toast('Removed from collection');
      }, function(err) { toast(err.message, true); });
    });
  });

  // --- Paste page ---
  (function() {
    var tools = $('.code-tools');
    if (!tools) return;
    var code = $('#codeblock'), id = tools.getAttribute('data-id');
    var cp = $('#codecopy');
    if (cp) cp.addEventListener('click', function() {
      var lines = $$('.cl', code), text;
      if (lines.length) text = lines.map(function(l) { return l.textContent; }).join('');
      else { var pre = $('pre', code); text = pre ? pre.textContent : code.textContent; }
      copyText(text).then(function() { feedback(cp, 'Copied'); }, function() { toast('Could not copy', true); });
    });
    var sel = $('#lang-select');
    if (sel) sel.addEventListener('change', function() {
      location.href = '/' + id + (sel.value ? '/' + encodeURIComponent(sel.value) : '');
    });
    var wrap = $('#wrap-toggle');
    function setWrap(on) {
      code.classList.toggle('wrap', on);
      wrap.setAttribute('aria-pressed', String(on));
      wrap.classList.toggle('active', on);
    }
    if (wrap) {
      setWrap(store('wrap') === '1');
      wrap.addEventListener('click', function() {
        var on = !code.classList.contains('wrap');
        setWrap(on);
        store('wrap', on ? '1' : null);
      });
    }
    var mdr = $('#md-rendered');
    $$('[data-md]', tools).forEach(function(b) {
      b.addEventListener('click', function() {
        var src = b.getAttribute('data-md') === 'source';
        mdr.hidden = src;
        code.hidden = !src;
        $$('[data-md]', tools).forEach(function(x) {
          var on = x === b;
          x.classList.toggle('active', on);
          x.setAttribute('aria-pressed', String(on));
        });
      });
    });
  })();

  // --- Upload page ---
  (function() {
    var form = $('#upload-form');
    if (!form) return;
    var maxSize = +form.getAttribute('data-max-size'), maxFiles = +form.getAttribute('data-max-files');
    var fi = $('#file-input'), fl = $('#file-list'), dz = $('#drop-zone'), ta = $('#paste-content'),
        hint = $('#upload-hint'), errBox = $('#upload-error'), colRow = $('#col-name-row'),
        prog = $('#progress'), bar = $('#progress-bar'), plabel = $('#progress-label'),
        btn = $('#upload-btn'), overlay = $('#drop-overlay');
    var files = [];

    function showErr(msg) { errBox.textContent = msg; errBox.hidden = !msg; }
    function count() { return files.length + (ta.value !== '' ? 1 : 0); }
    function total() {
      var n = ta.value !== '' ? new Blob([ta.value]).size : 0;
      files.forEach(function(f) { n += f.size; });
      return n;
    }

    function syncInput() {
      try {
        var dt = new DataTransfer();
        files.forEach(function(f) { dt.items.add(f); });
        fi.files = dt.files;
      } catch (e) {}
    }

    function stamp() {
      var d = new Date(), p = function(n) { return (n < 10 ? '0' : '') + n; };
      return d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds());
    }

    function addFiles(list, fromClipboard) {
      var rejected = 0;
      for (var i = 0; i < list.length; i++) {
        var f = list[i];
        if (count() >= maxFiles) { rejected++; continue; }
        if (fromClipboard && /^image\.\w+$/.test(f.name || 'image.png')) {
          var ext = (f.type.split('/')[1] || 'png').replace('jpeg', 'jpg').replace(/\+.*/, '');
          try { f = new File([f], 'paste-' + stamp() + (i ? '-' + i : '') + '.' + ext, { type: f.type }); } catch (e) {}
        }
        files.push(f);
      }
      render();
      if (rejected) showErr(rejected + ' file(s) not added: at most ' + maxFiles + ' files per upload.');
    }

    function render() {
      fl.innerHTML = '';
      files.forEach(function(f, i) {
        var chip = el('span', { 'class': 'file-chip' });
        chip.appendChild(el('span', { 'class': 'file-chip-name' }, f.name));
        chip.appendChild(el('span', { 'class': 'meta' }, fmtBytes(f.size)));
        var rm = el('button', { type: 'button', 'class': 'file-chip-rm', 'aria-label': 'Remove ' + f.name }, '×');
        rm.addEventListener('click', function() { files.splice(i, 1); render(); showErr(''); });
        chip.appendChild(rm);
        fl.appendChild(chip);
      });
      var n = count(), t = total(), left = maxFiles - n;
      hint.textContent = n
        ? n + ' item' + (n > 1 ? 's' : '') + ' · ' + fmtBytes(t) + ' of ' + fmtBytes(maxSize) + ' · ' + left + ' more allowed'
        : 'Up to ' + maxFiles + ' files, ' + fmtBytes(maxSize) + ' in total.';
      hint.classList.toggle('over', t > maxSize || n > maxFiles);
      colRow.hidden = n < 2;
      syncInput();
    }

    function validate() {
      var n = count(), t = total();
      if (!n) return 'Nothing to upload — paste some text or add files.';
      if (n > maxFiles) return 'Too many files: ' + n + ' (max ' + maxFiles + ' per upload).';
      if (t > maxSize) return 'Upload too large: ' + fmtBytes(t) + ' (max ' + fmtBytes(maxSize) + ').';
      return '';
    }

    fi.addEventListener('change', function() {
      if (!fi.files.length) return;
      var picked = Array.prototype.slice.call(fi.files);
      addFiles(picked);
    });
    dz.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fi.click(); }
    });
    ta.addEventListener('input', render);

    // drag & drop anywhere
    var depth = 0;
    function hasFiles(e) { return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types || [], 'Files') >= 0; }
    document.addEventListener('dragenter', function(e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      overlay.hidden = false;
      dz.classList.add('over');
    });
    document.addEventListener('dragover', function(e) { if (hasFiles(e)) e.preventDefault(); });
    document.addEventListener('dragleave', function(e) {
      if (!hasFiles(e)) return;
      if (--depth <= 0) { depth = 0; overlay.hidden = true; dz.classList.remove('over'); }
    });
    document.addEventListener('drop', function(e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      overlay.hidden = true;
      dz.classList.remove('over');
      showErr('');
      addFiles(e.dataTransfer.files);
    });

    // clipboard
    document.addEventListener('paste', function(e) {
      var cd = e.clipboardData;
      if (!cd) return;
      var got = [];
      for (var i = 0; i < (cd.items || []).length; i++) {
        if (cd.items[i].kind === 'file') { var f = cd.items[i].getAsFile(); if (f) got.push(f); }
      }
      if (!got.length && cd.files && cd.files.length) got = Array.prototype.slice.call(cd.files);
      if (got.length) { e.preventDefault(); showErr(''); addFiles(got, true); return; }
      var t = e.target;
      if (t === ta || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.isContentEditable) return;
      var txt = cd.getData('text');
      if (txt) { e.preventDefault(); ta.value += txt; ta.focus(); render(); }
    });

    form.addEventListener('submit', function(e) {
      var v = validate();
      if (v) { e.preventDefault(); showErr(v); return; }
      showErr('');
      if (!window.XMLHttpRequest || !window.FormData) { syncInput(); return; }
      e.preventDefault();
      var fd = new FormData(form);
      if (fd.delete) fd.delete('file');
      files.forEach(function(f) { fd.append('file', f, f.name); });
      var xhr = new XMLHttpRequest();
      xhr.open('POST', form.action);
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
      xhr.setRequestHeader('Accept', 'application/json');
      prog.hidden = false;
      btn.disabled = true;
      bar.style.width = '0';
      plabel.textContent = 'Starting…';
      xhr.upload.addEventListener('progress', function(ev) {
        if (!ev.lengthComputable) { plabel.textContent = fmtBytes(ev.loaded) + ' sent'; return; }
        var p = Math.round(ev.loaded / ev.total * 100);
        bar.style.width = p + '%';
        plabel.textContent = fmtBytes(ev.loaded) + ' / ' + fmtBytes(ev.total) + ' (' + p + '%)';
      });
      xhr.addEventListener('load', function() {
        var j = {};
        try { j = JSON.parse(xhr.responseText); } catch (er) {}
        if (xhr.status >= 200 && xhr.status < 300 && j.url) {
          plabel.textContent = 'Done — opening…';
          location.href = j.url;
          return;
        }
        prog.hidden = true;
        btn.disabled = false;
        showErr(j.error || ('Upload failed (' + xhr.status + ')'));
      });
      xhr.addEventListener('error', function() {
        prog.hidden = true;
        btn.disabled = false;
        showErr('Network error — upload failed.');
      });
      xhr.send(fd);
    });
    render();
  })();

  // --- History page ---
  (function() {
    var table = $('#hist');
    if (!table) return;
    var tbody = $('tbody', table), rows = $$('tr[data-id]', tbody);
    var search = $('#h-search'), sortSel = $('#h-sort'), empty = $('#h-empty');
    var form = $('#hform'), btnDel = $('#btn-del'), btnCol = $('#btn-col'), btnAdd = $('#btn-add'),
        selCount = $('#sel-count'), ckall = $('#ckall');
    var filter = '';

    function visible() { return rows.filter(function(r) { return !r.hidden; }); }
    function checked() { return rows.filter(function(r) { return $('input[name=ids]', r).checked; }); }

    function apply() {
      var q = search.value.trim().toLowerCase();
      rows.forEach(function(r) {
        var okT = !filter || r.getAttribute('data-kind') === filter;
        var okQ = !q || r.getAttribute('data-name').toLowerCase().indexOf(q) >= 0 || r.getAttribute('data-id').indexOf(q) >= 0;
        r.hidden = !(okT && okQ);
      });
      var by = sortSel.value;
      rows.slice().sort(function(a, b) {
        if (by === 'name') return a.getAttribute('data-name').localeCompare(b.getAttribute('data-name'), undefined, { numeric: true, sensitivity: 'base' });
        if (by === 'size') return b.getAttribute('data-size') - a.getAttribute('data-size');
        return b.getAttribute('data-date') - a.getAttribute('data-date');
      }).forEach(function(r) { tbody.appendChild(r); });
      if (rows.length) empty.hidden = visible().length > 0;
      update();
    }

    function update() {
      var c = checked(), files = c.filter(function(r) { return !r.hasAttribute('data-col'); });
      btnDel.disabled = !c.length;
      btnCol.disabled = !files.length;
      if (btnAdd) btnAdd.disabled = !files.length;
      selCount.textContent = c.length ? c.length + ' selected' : '';
      var v = visible();
      ckall.checked = v.length > 0 && v.every(function(r) { return $('input[name=ids]', r).checked; });
      rows.forEach(function(r) { r.classList.toggle('selected', $('input[name=ids]', r).checked); });
    }

    search.addEventListener('input', apply);
    sortSel.addEventListener('change', function() { store('hsort', sortSel.value); apply(); });
    var savedSort = store('hsort');
    if (savedSort) sortSel.value = savedSort;
    $$('.chip').forEach(function(c) {
      c.addEventListener('click', function() {
        filter = c.getAttribute('data-filter');
        $$('.chip').forEach(function(x) { x.classList.toggle('active', x === c); x.setAttribute('aria-pressed', String(x === c)); });
        apply();
      });
    });
    ckall.addEventListener('change', function() {
      visible().forEach(function(r) { $('input[name=ids]', r).checked = ckall.checked; });
      update();
    });
    tbody.addEventListener('change', update);

    function fileIDs() {
      return checked().filter(function(r) { return !r.hasAttribute('data-col'); }).map(function(r) { return r.getAttribute('data-id'); });
    }

    btnDel.addEventListener('click', function() {
      var c = checked();
      if (!c.length) return;
      var hasCol = c.some(function(r) { return r.hasAttribute('data-col'); });
      var chk = hasCol ? checkField('Also delete files inside collections') : null;
      modal({
        title: 'Delete ' + c.length + ' item' + (c.length > 1 ? 's' : '') + '?',
        desc: 'This cannot be undone.', danger: true, confirm: 'Delete',
        body: chk ? chk.wrap : null,
        onConfirm: function() {
          $('#delete_files').value = chk && chk.input.checked ? '1' : '0';
          form.action = '/delete';
          form.submit();
        }
      });
    });

    btnCol.addEventListener('click', function() {
      var ids = fileIDs();
      if (!ids.length) return;
      var body = el('div');
      var name = field('Name', { type: 'text', placeholder: 'optional' });
      var pw = field('Password', { type: 'password', placeholder: 'optional', autocomplete: 'new-password' });
      body.appendChild(name.wrap);
      body.appendChild(pw.wrap);
      modal({
        title: 'New collection from ' + ids.length + ' file' + (ids.length > 1 ? 's' : ''),
        body: body, confirm: 'Create',
        onConfirm: function() {
          var f = el('form', { method: 'post', action: '/collection' });
          function hid(n, v) { var i = el('input', { type: 'hidden', name: n }); i.value = v; f.appendChild(i); }
          hid('csrf_token', $('input[name=csrf_token]', form).value);
          hid('name', name.input.value);
          hid('password', pw.input.value);
          ids.forEach(function(id) { hid('ids', id); });
          document.body.appendChild(f);
          f.submit();
        }
      });
    });

    if (btnAdd) btnAdd.addEventListener('click', function() {
      var ids = fileIDs();
      if (!ids.length) return;
      var sel = field('Collection', { tag: 'select' });
      sel.input.innerHTML = $('#col-options').innerHTML;
      modal({
        title: 'Add ' + ids.length + ' file' + (ids.length > 1 ? 's' : '') + ' to collection',
        body: sel.wrap, confirm: 'Add',
        onConfirm: function() {
          return api('PATCH', '/api/' + sel.input.value, { add: ids }).then(function() {
            location.href = '/' + sel.input.value;
          });
        }
      });
    });

    apply();
  })();

  window.filebin = { api: api, modal: modal, toast: toast, copy: copyText };
})();
