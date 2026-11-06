'use strict';
// Runs in <head> before the page draws, so a chosen theme shows without a flash.
// Settings → Appearance stores the choice in this browser only.
(function () {
  try {
    var t = localStorage.getItem('outpost.theme');
    if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
  } catch (e) { /* storage blocked: follow the system */ }
})();
