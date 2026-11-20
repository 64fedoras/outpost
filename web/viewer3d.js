'use strict';
// A small WebGL viewer for STL files (New 3D model page). No library: parse,
// flat-shade with a head light, orbit with drag, zoom with the wheel or a pinch,
// double-click to reset, optional wireframe. OpenSCAD's Z axis points up.

// parseSTL returns { pos: Float32Array (x,y,z per vertex), nrm: Float32Array, tris, min, max }.
function parseSTL(buf) {
  const dv = new DataView(buf);
  let pos;
  if (buf.byteLength >= 84 && 84 + 50 * dv.getUint32(80, true) === buf.byteLength) {
    const n = dv.getUint32(80, true);
    pos = new Float32Array(n * 9);
    for (let t = 0; t < n; t++) {
      const off = 84 + 50 * t + 12;
      for (let k = 0; k < 9; k++) pos[t * 9 + k] = dv.getFloat32(off + 4 * k, true);
    }
  } else {
    const text = new TextDecoder().decode(buf), out = [];
    const re = /vertex\s+(\S+)\s+(\S+)\s+(\S+)/g;
    let m;
    while ((m = re.exec(text))) out.push(+m[1], +m[2], +m[3]);
    pos = new Float32Array(out.length - (out.length % 9));
    pos.set(out.slice(0, pos.length));
  }
  const nrm = new Float32Array(pos.length);
  const min = [Infinity, Infinity, Infinity], max = [-Infinity, -Infinity, -Infinity];
  for (let i = 0; i < pos.length; i += 9) {
    const ax = pos[i + 3] - pos[i], ay = pos[i + 4] - pos[i + 1], az = pos[i + 5] - pos[i + 2];
    const bx = pos[i + 6] - pos[i], by = pos[i + 7] - pos[i + 1], bz = pos[i + 8] - pos[i + 2];
    let nx = ay * bz - az * by, ny = az * bx - ax * bz, nz = ax * by - ay * bx;
    const l = Math.hypot(nx, ny, nz) || 1; nx /= l; ny /= l; nz /= l;
    for (let k = 0; k < 9; k += 3) {
      nrm[i + k] = nx; nrm[i + k + 1] = ny; nrm[i + k + 2] = nz;
      for (let a = 0; a < 3; a++) { const v = pos[i + k + a]; if (v < min[a]) min[a] = v; if (v > max[a]) max[a] = v; }
    }
  }
  return { pos, nrm, tris: pos.length / 9, min, max };
}

const M4 = {
  persp(fovy, aspect, near, far) {
    const f = 1 / Math.tan(fovy / 2), nf = 1 / (near - far);
    return [f / aspect, 0, 0, 0, 0, f, 0, 0, 0, 0, (far + near) * nf, -1, 0, 0, 2 * far * near * nf, 0];
  },
  lookAt(eye, at, up) {
    let zx = eye[0] - at[0], zy = eye[1] - at[1], zz = eye[2] - at[2];
    let l = Math.hypot(zx, zy, zz) || 1; zx /= l; zy /= l; zz /= l;
    let xx = up[1] * zz - up[2] * zy, xy = up[2] * zx - up[0] * zz, xz = up[0] * zy - up[1] * zx;
    l = Math.hypot(xx, xy, xz) || 1; xx /= l; xy /= l; xz /= l;
    const yx = zy * xz - zz * xy, yy = zz * xx - zx * xz, yz = zx * xy - zy * xx;
    return [xx, yx, zx, 0, xy, yy, zy, 0, xz, yz, zz, 0,
      -(xx * eye[0] + xy * eye[1] + xz * eye[2]), -(yx * eye[0] + yy * eye[1] + yz * eye[2]), -(zx * eye[0] + zy * eye[1] + zz * eye[2]), 1];
  },
  mul(a, b) { // a * b, column-major
    const o = new Array(16);
    for (let c = 0; c < 4; c++) for (let r = 0; r < 4; r++) {
      let s = 0;
      for (let k = 0; k < 4; k++) s += a[k * 4 + r] * b[c * 4 + k];
      o[c * 4 + r] = s;
    }
    return o;
  },
};

const VS = `attribute vec3 p; attribute vec3 n; uniform mat4 mvp; uniform mat4 mv; varying vec3 vn;
void main() { vn = (mv * vec4(n, 0.0)).xyz; gl_Position = mvp * vec4(p, 1.0); }`;
const FS = `precision mediump float; varying vec3 vn; uniform vec3 col; uniform float uflat;
void main() { if (uflat > 0.5) { gl_FragColor = vec4(col, 1.0); return; }
  float d = abs(normalize(vn).z); gl_FragColor = vec4(col * (0.28 + 0.72 * d), 1.0); }`;

// Viewer3D(canvas) draws one mesh. Returns null when WebGL is not available.
function Viewer3D(canvas) {
  const gl = canvas.getContext('webgl', { antialias: true, preserveDrawingBuffer: true });
  if (!gl) return null;
  const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, VS)); gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, FS)); gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) { console.warn('3D viewer: ' + gl.getProgramInfoLog(prog)); return null; }
  const loc = { p: gl.getAttribLocation(prog, 'p'), n: gl.getAttribLocation(prog, 'n'), mvp: gl.getUniformLocation(prog, 'mvp'), mv: gl.getUniformLocation(prog, 'mv'), col: gl.getUniformLocation(prog, 'col'), flat: gl.getUniformLocation(prog, 'uflat') };
  const bufs = { p: gl.createBuffer(), n: gl.createBuffer(), lp: gl.createBuffer(), ln: gl.createBuffer() };
  const v = { mesh: null, yaw: -0.7, pitch: 0.55, dist: 3.8, center: [0, 0, 0], radius: 1, wire: false, lines: 0, drag: null, pts: new Map() };

  function color(name, fallback) {
    const s = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    const m = s.match(/^#([0-9a-f]{6})$/i);
    if (!m) return fallback;
    const n = parseInt(m[1], 16);
    return [(n >> 16 & 255) / 255, (n >> 8 & 255) / 255, (n & 255) / 255];
  }

  function draw() {
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(1, Math.round(canvas.clientWidth * dpr)), h = Math.max(1, Math.round(canvas.clientHeight * dpr));
    if (canvas.width !== w || canvas.height !== h) { canvas.width = w; canvas.height = h; }
    gl.viewport(0, 0, w, h);
    gl.clearColor(0, 0, 0, 0); gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    if (!v.mesh) return;
    gl.enable(gl.DEPTH_TEST);
    gl.useProgram(prog);
    const r = v.radius * v.dist, c = v.center;
    const eye = [c[0] + r * Math.cos(v.pitch) * Math.cos(v.yaw), c[1] + r * Math.cos(v.pitch) * Math.sin(v.yaw), c[2] + r * Math.sin(v.pitch)];
    const mv = M4.lookAt(eye, c, [0, 0, 1]);
    const pr = M4.persp(0.7, w / h, Math.max(0.01, r - v.radius * 2), r + v.radius * 2);
    gl.uniformMatrix4fv(loc.mvp, false, new Float32Array(M4.mul(pr, mv)));
    gl.uniformMatrix4fv(loc.mv, false, new Float32Array(mv));
    const bind = (b, l) => { gl.bindBuffer(gl.ARRAY_BUFFER, b); gl.enableVertexAttribArray(l); gl.vertexAttribPointer(l, 3, gl.FLOAT, false, 0, 0); };
    if (v.wire) {
      bind(bufs.lp, loc.p); bind(bufs.ln, loc.n);
      gl.uniform3fv(loc.col, color('--ink', [0.2, 0.2, 0.2])); gl.uniform1f(loc.flat, 1);
      gl.drawArrays(gl.LINES, 0, v.lines);
    } else {
      bind(bufs.p, loc.p); bind(bufs.n, loc.n);
      gl.uniform3fv(loc.col, color('--accent', [0.36, 0.55, 0.85])); gl.uniform1f(loc.flat, 0);
      gl.drawArrays(gl.TRIANGLES, 0, v.mesh.tris * 3);
    }
  }
  let queued = false;
  const redraw = () => { if (!queued) { queued = true; requestAnimationFrame(() => { queued = false; draw(); }); } };

  function load(buf) {
    const m = parseSTL(buf);
    v.mesh = m;
    v.center = [0, 1, 2].map((i) => (m.min[i] + m.max[i]) / 2);
    v.radius = Math.max(1e-3, Math.hypot(m.max[0] - m.min[0], m.max[1] - m.min[1], m.max[2] - m.min[2]) / 2);
    gl.bindBuffer(gl.ARRAY_BUFFER, bufs.p); gl.bufferData(gl.ARRAY_BUFFER, m.pos, gl.STATIC_DRAW);
    gl.bindBuffer(gl.ARRAY_BUFFER, bufs.n); gl.bufferData(gl.ARRAY_BUFFER, m.nrm, gl.STATIC_DRAW);
    v.lines = 0; // built when wireframe is first shown
    if (v.wire) buildLines();
    redraw();
    return m;
  }
  function buildLines() {
    if (!v.mesh || v.lines) return;
    const p = v.mesh.pos, lp = new Float32Array(p.length * 2);
    for (let i = 0, o = 0; i < p.length; i += 9) {
      for (const [a, b] of [[0, 3], [3, 6], [6, 0]]) { for (let k = 0; k < 3; k++) lp[o + k] = p[i + a + k]; for (let k = 0; k < 3; k++) lp[o + 3 + k] = p[i + b + k]; o += 6; }
    }
    gl.bindBuffer(gl.ARRAY_BUFFER, bufs.lp); gl.bufferData(gl.ARRAY_BUFFER, lp, gl.STATIC_DRAW);
    gl.bindBuffer(gl.ARRAY_BUFFER, bufs.ln); gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(lp.length), gl.STATIC_DRAW);
    v.lines = lp.length / 3;
  }

  // Orbit and zoom.
  canvas.addEventListener('pointerdown', (e) => { canvas.setPointerCapture(e.pointerId); v.pts.set(e.pointerId, [e.clientX, e.clientY]); });
  canvas.addEventListener('pointermove', (e) => {
    const old = v.pts.get(e.pointerId);
    if (!old) return;
    if (v.pts.size === 2) {
      const [a, b] = [...v.pts.values()];
      const before = Math.hypot(a[0] - b[0], a[1] - b[1]);
      v.pts.set(e.pointerId, [e.clientX, e.clientY]);
      const [c, d] = [...v.pts.values()];
      const after = Math.hypot(c[0] - d[0], c[1] - d[1]);
      if (before > 0 && after > 0) v.dist = Math.min(20, Math.max(1.05, v.dist * before / after));
    } else {
      v.yaw -= (e.clientX - old[0]) * 0.01;
      v.pitch = Math.min(1.55, Math.max(-1.55, v.pitch + (e.clientY - old[1]) * 0.01));
      v.pts.set(e.pointerId, [e.clientX, e.clientY]);
    }
    redraw();
  });
  const up = (e) => v.pts.delete(e.pointerId);
  canvas.addEventListener('pointerup', up); canvas.addEventListener('pointercancel', up);
  canvas.addEventListener('wheel', (e) => { e.preventDefault(); v.dist = Math.min(20, Math.max(1.05, v.dist * Math.exp(e.deltaY * 0.001))); redraw(); }, { passive: false });
  canvas.addEventListener('dblclick', () => reset());
  function reset() { v.yaw = -0.7; v.pitch = 0.55; v.dist = 3.8; redraw(); }
  const ro = typeof ResizeObserver === 'function' ? new ResizeObserver(redraw) : null;
  if (ro) ro.observe(canvas);
  // Follow a theme switch: the colours come from the CSS tokens.
  const mo = new MutationObserver(redraw);
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

  return {
    load, reset, redraw,
    setWire(on) { v.wire = !!on; if (v.wire) buildLines(); redraw(); },
    get wire() { return v.wire; },
    dispose() { if (ro) ro.disconnect(); mo.disconnect(); const ext = gl.getExtension('WEBGL_lose_context'); if (ext) ext.loseContext(); },
  };
}
