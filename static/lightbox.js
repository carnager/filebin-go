(function() {
  var items = Array.prototype.slice.call(document.querySelectorAll('.gallery-item'));
  if (!items.length) return;

  var lb = document.createElement('div');
  lb.className = 'lightbox';
  lb.setAttribute('role', 'dialog');
  lb.setAttribute('aria-modal', 'true');
  lb.setAttribute('aria-label', 'Image viewer');
  lb.hidden = true;
  lb.innerHTML =
    '<button type="button" class="lightbox-close" aria-label="Close">&times;</button>' +
    '<button type="button" class="lightbox-prev" aria-label="Previous image">&#8249;</button>' +
    '<button type="button" class="lightbox-next" aria-label="Next image">&#8250;</button>' +
    '<div class="lightbox-content"><img alt="">' +
    '<div class="lightbox-caption"></div>' +
    '<div class="lightbox-links"><a class="lb-open" target="_blank" rel="noopener">Open original</a>' +
    '<a class="lb-dl">Download</a><span class="lightbox-counter"></span></div></div>';
  document.body.appendChild(lb);

  var img = lb.querySelector('img'), cap = lb.querySelector('.lightbox-caption'),
      ctr = lb.querySelector('.lightbox-counter'), openA = lb.querySelector('.lb-open'),
      dlA = lb.querySelector('.lb-dl'), closeB = lb.querySelector('.lightbox-close'),
      prevB = lb.querySelector('.lightbox-prev'), nextB = lb.querySelector('.lightbox-next');
  var idx = 0, last = null, preload = [];
  var urls = items.map(function(el) { return el.getAttribute('data-raw'); });
  var names = items.map(function(el) { return el.getAttribute('data-name'); });

  if (items.length < 2) { prevB.hidden = true; nextB.hidden = true; }

  items.forEach(function(el, i) {
    el.addEventListener('click', function(e) {
      if (e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
      e.preventDefault();
      open(i);
    });
  });

  function open(i) {
    last = document.activeElement;
    idx = i;
    show();
    lb.hidden = false;
    requestAnimationFrame(function() { lb.classList.add('open'); });
    document.body.style.overflow = 'hidden';
    closeB.focus();
  }
  function close() {
    lb.classList.remove('open');
    lb.hidden = true;
    img.removeAttribute('src');
    document.body.style.overflow = '';
    if (last && last.focus) last.focus();
  }
  function show() {
    img.src = urls[idx];
    img.alt = names[idx] || '';
    cap.textContent = names[idx];
    ctr.textContent = items.length > 1 ? (idx + 1) + ' / ' + urls.length : '';
    openA.href = urls[idx];
    dlA.href = urls[idx] + '?download=1';
    preload = [-1, 1].map(function(d) {
      var p = new Image();
      p.src = urls[(idx + d + urls.length) % urls.length];
      return p;
    });
  }
  function prev() { idx = (idx - 1 + urls.length) % urls.length; show(); }
  function next() { idx = (idx + 1) % urls.length; show(); }

  closeB.addEventListener('click', close);
  prevB.addEventListener('click', prev);
  nextB.addEventListener('click', next);
  lb.addEventListener('click', function(e) {
    if (e.target === lb || e.target.classList.contains('lightbox-content')) close();
  });

  document.addEventListener('keydown', function(e) {
    if (lb.hidden) return;
    if (e.key === 'Escape') close();
    else if (e.key === 'ArrowLeft') prev();
    else if (e.key === 'ArrowRight') next();
    else if (e.key === 'Tab') {
      var f = Array.prototype.filter.call(lb.querySelectorAll('button,a[href]'), function(el) { return !el.hidden; });
      var first = f[0], lastEl = f[f.length - 1];
      if (!lb.contains(document.activeElement)) { e.preventDefault(); first.focus(); }
      else if (e.shiftKey && document.activeElement === first) { e.preventDefault(); lastEl.focus(); }
      else if (!e.shiftKey && document.activeElement === lastEl) { e.preventDefault(); first.focus(); }
    }
  });

  var sx = 0, sy = 0, tracking = false;
  lb.addEventListener('touchstart', function(e) {
    if (e.touches.length !== 1) { tracking = false; return; }
    tracking = true;
    sx = e.touches[0].clientX;
    sy = e.touches[0].clientY;
  }, { passive: true });
  lb.addEventListener('touchmove', function(e) {
    if (!tracking) return;
    var dy = e.touches[0].clientY - sy;
    if (dy > 0 && Math.abs(dy) > Math.abs(e.touches[0].clientX - sx)) {
      img.style.transform = 'translateY(' + dy + 'px)';
      img.style.opacity = Math.max(0.3, 1 - dy / 400);
    }
  }, { passive: true });
  lb.addEventListener('touchend', function(e) {
    if (!tracking) return;
    tracking = false;
    var dx = e.changedTouches[0].clientX - sx, dy = e.changedTouches[0].clientY - sy;
    img.style.transform = '';
    img.style.opacity = '';
    if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy)) { if (dx < 0) next(); else prev(); }
    else if (dy > 90 && Math.abs(dy) > Math.abs(dx)) close();
  });
})();
