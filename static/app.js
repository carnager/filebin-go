document.addEventListener('DOMContentLoaded', function() {
  var fi = document.getElementById('file-input');
  var fl = document.getElementById('file-list');
  var dz = document.getElementById('drop-zone');
  var addBtn = document.getElementById('add-more-btn');
  if (!fi || !fl || !dz) return;

  var dt = new DataTransfer();

  function addFiles(files) {
    for (var i = 0; i < files.length; i++) dt.items.add(files[i]);
    fi.files = dt.files;
    render();
  }

  function removeFile(idx) {
    var ndt = new DataTransfer();
    for (var i = 0; i < dt.files.length; i++) {
      if (i !== idx) ndt.items.add(dt.files[i]);
    }
    dt = ndt;
    fi.files = dt.files;
    render();
  }

  function render() {
    fl.innerHTML = '';
    if (dt.files.length === 0) {
      if (addBtn) addBtn.style.display = 'none';
      return;
    }
    if (addBtn) addBtn.style.display = '';
    for (var i = 0; i < dt.files.length; i++) {
      var chip = document.createElement('span');
      chip.className = 'file-chip';
      chip.textContent = dt.files[i].name;
      var rm = document.createElement('button');
      rm.type = 'button';
      rm.className = 'file-chip-rm';
      rm.textContent = '\u00d7';
      rm.setAttribute('data-idx', i);
      rm.addEventListener('click', function() { removeFile(parseInt(this.getAttribute('data-idx'))); });
      chip.appendChild(rm);
      fl.appendChild(chip);
    }
  }

  fi.addEventListener('change', function() {
    addFiles(fi.files);
  });

  if (addBtn) {
    addBtn.addEventListener('click', function() {
      fi.click();
    });
  }

  ['dragenter','dragover'].forEach(function(e) {
    dz.addEventListener(e, function(ev) { ev.preventDefault(); dz.classList.add('over'); });
  });
  ['dragleave','drop'].forEach(function(e) {
    dz.addEventListener(e, function(ev) { ev.preventDefault(); dz.classList.remove('over'); });
  });
  dz.addEventListener('drop', function(ev) {
    ev.preventDefault();
    addFiles(ev.dataTransfer.files);
  });
});
