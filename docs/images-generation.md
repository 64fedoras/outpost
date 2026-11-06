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

## Using the New image page

Open **New image** in the sidebar (or press Ctrl+K and type "image"). Type a prompt, choose a model, a size and
how many pictures, then **Generate**. **Change options** adds steps, cfg, a seed and a negative prompt. Running
generations show the node and the step; **Stop** cancels them. Finished ones appear under **Results** with
Download, **Variations** (same settings, new seeds), **Use prompt**, **Keep** (never deleted automatically),
**Retry failed** and Delete; older ones sit in a compact grid. **Image models** at the bottom lists the models,
their licence and which nodes they fit, and downloads one to a node of your choice. A model with a restricted
licence asks for an accept click first. **Settings, Model settings, Pictures** sets how long (default 30 days)
and how much (default 5 GB) to keep. The page was checked in a headless browser against stand-in data only.

## The OpenAI-style image API

Off by default. Turn it on under **Settings, Model settings, Pictures** ("Allow the image API"), and make an API
token with **Chat and pictures** ticked (or use a full-access token). A chat-only token is refused.

```sh
curl -sk https://192.0.2.10:8443/v1/images/generations \
  -H "Authorization: Bearer opt_..." -H "Content-Type: application/json" \
  -d '{"model":"flux1-schnell-q8","prompt":"a stone outpost at dusk","n":2,"size":"1024x1024"}'
```

`model` is an id from `GET /v1/models` (image models are listed while the API is on), `n` is 1 to 16,
`size` is `WxH`, and `response_format` is `b64_json` (default) or `url` (a `/v1/images/files/...` link that needs
the same token). Extras: `seed`, `steps`, `negative_prompt`. The call waits until the pictures are done (up to
15 minutes) and cancels them if the client disconnects. The pictures also appear on the New image page, marked
with the token's name, and follow the same retention rules. `/v1/images/edits` is not built.
**Untested** with real clients (Open WebUI, the OpenAI SDKs) and real nodes.

## Video

The **Video** switch on the New image page makes short clips with the same machinery. The built-in model is
**Wan 2.1 text-to-video 1.3B**; pick a size (832 × 480 is what it was trained at), a length (1, 2, 4 or 5 seconds
at 16 frames a second) and one or two clips. A clip takes minutes, not seconds, and uses one node for the whole
time; the page shows how long it has been working (sd-server reports no step progress for video). Clips play in
the page, with Download, Keep and the rest as for pictures, and follow the same retention rules. The node asks
sd-server for WebM and falls back to AVI if that build has no WebM support. A longer clip needs more memory, so
the fit check on the Models list and the "no node can run it" message take the length into account.

Video is not part of the OpenAI-style API (`/v1/images/generations` refuses video models). It needs nodes on this
version or newer (`node.video`). **Untested:** all of it on real hardware. Two guesses to check first with
`sd-server -h` on a node: the arguments that start sd-server in video mode (the node passes `-M vid_gen`; set
`OUTPOST_SD_VIDEO_ARGS` on the node to change them, or `none` for no extra arguments), and the Hugging Face file
names in the catalog (Comfy-Org's Wan 2.1 repackaged files and city96's UMT5 encoder). The memory numbers for clips are
rough guesses.
