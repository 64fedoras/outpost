'use strict';
// Starts the UI. Loaded last so every page's code exists before the first state reply is handled.
api('/api/state').then(showApp).catch(() => {});
