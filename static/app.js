document.addEventListener('DOMContentLoaded', function() {
  var fi = document.getElementById('file-input');
  var fl = document.getElementById('file-list');
  var dz = document.getElementById('drop-zone');
  if (!fi || !fl || !dz) return;

  fi.addEventListener('change', function() {
    var names = [];
    for (var i = 0; i < fi.files.length; i++) names.push(fi.files[i].name);
    fl.textContent = names.join(', ');
  });

  ['dragenter','dragover'].forEach(function(e) {
    dz.addEventListener(e, function(ev) { ev.preventDefault(); dz.classList.add('over'); });
  });
  ['dragleave','drop'].forEach(function(e) {
    dz.addEventListener(e, function(ev) { ev.preventDefault(); dz.classList.remove('over'); });
  });
  dz.addEventListener('drop', function(ev) {
    ev.preventDefault();
    fi.files = ev.dataTransfer.files;
    fi.dispatchEvent(new Event('change'));
  });
});
