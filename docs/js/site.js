// Nexler docs — site.js
// Mobile TOC drawer toggle, scroll-spy for the sidebar, and copy-to-clipboard
// buttons on code blocks. No dependencies, no build step — edit in place.

(function () {
  'use strict';

  // ---------- Mobile TOC drawer ----------
  var toggle = document.getElementById('toc-toggle');
  if (toggle) {
    toggle.addEventListener('click', function () {
      var isOpen = document.body.classList.toggle('toc-open');
      toggle.setAttribute('aria-expanded', String(isOpen));
      toggle.setAttribute('aria-label', isOpen ? 'Close contents' : 'Open contents');
    });
    var sidebar = document.getElementById('docs-sidebar');
    if (sidebar) {
      sidebar.addEventListener('click', function (e) {
        if (e.target.tagName === 'A') {
          document.body.classList.remove('toc-open');
          toggle.setAttribute('aria-expanded', 'false');
          toggle.setAttribute('aria-label', 'Open contents');
        }
      });
    }
  }

  // ---------- Scroll-spy: highlight the active sidebar link ----------
  var tocLinks = Array.prototype.slice.call(document.querySelectorAll('.toc-group a'));
  var sections = tocLinks
    .map(function (a) {
      var id = a.getAttribute('href').replace('#', '');
      return { link: a, el: document.getElementById(id) };
    })
    .filter(function (s) { return s.el; });

  function updateActive() {
    var pos = window.scrollY + 120;
    var current = sections[0];
    for (var i = 0; i < sections.length; i++) {
      if (sections[i].el.offsetTop <= pos) current = sections[i];
    }
    tocLinks.forEach(function (a) { a.classList.remove('active'); });
    if (current) current.link.classList.add('active');
  }
  if (sections.length) {
    document.addEventListener('scroll', throttle(updateActive, 100), { passive: true });
    window.addEventListener('load', updateActive);
    updateActive();
  }

  function throttle(fn, wait) {
    var last = 0, timer = null;
    return function () {
      var now = Date.now();
      if (now - last >= wait) { last = now; fn(); }
      else {
        clearTimeout(timer);
        timer = setTimeout(function () { last = Date.now(); fn(); }, wait - (now - last));
      }
    };
  }

  // ---------- Copy-to-clipboard on code blocks ----------
  document.querySelectorAll('.code-block').forEach(function (block) {
    var btn = document.createElement('button');
    btn.className = 'copy-btn';
    btn.type = 'button';
    btn.textContent = 'Copy';
    btn.addEventListener('click', function () {
      var codeEl = block.querySelector('pre');
      var text = codeEl ? codeEl.innerText : '';
      copyText(text).then(function () {
        btn.textContent = 'Copied';
        btn.classList.add('copied');
        setTimeout(function () {
          btn.textContent = 'Copy';
          btn.classList.remove('copied');
        }, 1400);
      });
    });
    block.appendChild(btn);
  });

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).catch(function () { fallbackCopy(text); });
    }
    fallbackCopy(text);
    return Promise.resolve();
  }

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (e) { /* no-op */ }
    document.body.removeChild(ta);
  }
})();
