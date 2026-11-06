# Image generation (backend; the New image page follows)

Nodes can make pictures with [stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp)
(MIT licence), installed and run by Outpost the way it handles llama.cpp: the controller keeps a copy of the
release, a node downloads it, and the node starts `sd-server` for a picture and stops it after 3 idle
minutes. The node keeps the runtime and the model files and deletes every picture as soon as the controller
has it; results live on the controller under `generations/` and are removed after **30 days** or when they
pass **5 GB** (oldest first; both limits can be changed, 0 means no limit; a result marked *keep* stays).

Built-in models: FLUX.1 schnell (Apache-2.0), SDXL-Turbo (non-commercial licence, needs an accept click) and
Z-Image Turbo (Apache-2.0). Model files download straight from Hugging Face to the node. The memory estimate
knows that a picture model is several files and that its text encoders can wait in system RAM
(`--offload-to-cpu`) while the picture is drawn; the real compute buffer a node reports after a run replaces
the guess for that node and size. Asking for a picture unloads that node's chat model (it loads again on the
next request). Several pictures in one request are spread over every fit node.

API (same sign-in or API token as the rest): `GET /api/images/models`, `POST /api/images`
(`{"model","prompt","count","width","height","steps","cfg","seed"}`), `GET /api/images`,
`GET /api/images/{id}`, `GET /api/images/{id}/files/{n}`, `POST /api/images/{id}/cancel|retry|keep`,
`DELETE /api/images/{id}`, `GET|POST /api/images/settings`. Nodes need this version (`node.gen`).
**Untested on real hardware:** Vulkan on AMD cards, memory numbers, speeds, Windows and macOS. Everything was
tested against a stand-in `sd-server`.
