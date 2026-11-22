# 3D models (scripted CAD)

The **New 3D model** page turns a description into a printable part. A chat model on a node writes an
[OpenSCAD](https://openscad.org) file, an exec node that has OpenSCAD renders it to an STL, and the page shows
the result in a 3D view you can rotate. It suits precise parts with sizes: brackets, boxes, stands, clips,
knobs. It does not make organic shapes or textured meshes (no neural 3D model is used).

## What a node needs

- **A chat model loaded** on at least one node, as for Chat. Coding models write better OpenSCAD.
- **OpenSCAD installed** on a node with exec enabled. Outpost does not install it: use
  `sudo apt install openscad` (Debian/Ubuntu), `sudo dnf install openscad` (Fedora), `brew install --cask openscad`
  (macOS) or the installer from openscad.org (Windows, found in `C:\Program Files\OpenSCAD`). The node finds it
  within an hour (restart the node to see it at once) and lists it under its tools on the Nodes page. Rendering runs on the CPU,
  so a small node is fine.

## Using the page

Open **Make a 3D model** from the **＋** menu in Chat (it asks you to confirm, and **More settings…** opens this page), or press Ctrl+K and type "3D". Describe the part with its sizes in millimetres and press **Design**
(Ctrl+Enter). Options in the bar:
- **Model**: the strongest free node, or a node's loaded model.
- **Check the size**: after the first good render the model is told the measured size (bounding box and triangle
  count) and may correct the file once.
- **Up to N rounds**: how many times the model may write or fix the file. When OpenSCAD reports an error, the
  error and the file go back to the model for the next round.

**Measurements from the web.** Started from the chat card, two boxes can run before the model writes the file:
**Read the linked page** (on when your text has a link; the page text is fetched by the controller, never from an
internal address) and **Look up the measurements of the devices named here** (on when there is a link or a part
number such as GL-AR750). The look-up asks the model for up to two search queries, runs them with the same free
search as chat (DuckDuckGo, or Brave with a key), reads the first result that opens, and asks the model to pull out
the measurements with their sources. They are shown under the result as **Measurements used**, marked "from the
web, check before printing", and the model is told to make each one a named parameter so a wrong number is one
**Parameters** change away from right. Your own numbers in the description win. If nothing is found, the steps say
so and the sizes are guesses. API: `POST /api/cad` takes `read_links` and `lookup` (both false by default); the
model record has `refs`. Outpost cannot edit an STL or 3MF; a link to one is used only as a description.
**Untested:** real search results and real models picking good numbers (tested with a search stand-in and a stub model).

**Now working** shows the round and the step; **Stop** ends it. The **Result** shows the 3D view (drag to rotate,
scroll or pinch to zoom, double-click to reset, **Wireframe**), the OpenSCAD file, and:
- **Parameters**: the numeric and true/false variables at the top of the file. Change them and press
  **Render with these values** to render again without the model; **Back to the file's values** undoes that.
- **Ask for changes**: tell the model what to change; it gets the current file, with your parameter values
  written into it.
- **Download STL** (binary STL for the slicer) and **Download .scad** (edit it in OpenSCAD yourself),
  **Use description**, **Keep** and Delete. **Steps** lists what happened in each round.

Earlier models are listed below; click one to open it.

## Storage and privacy

Files live on the controller in `cad/<id>/` (`meta.json`, `model.scad`, `model.stl`). The render node gets the
`.scad` inside an exec job and keeps nothing: the job folder is deleted once the STL is back. Models older than
the picture retention days (Settings, Pictures, default 30) are deleted unless kept; the size cap for pictures
does not count them.

API (same sign-in or API token): `GET /api/cad`, `POST /api/cad` (`{"prompt","node","rounds","check"}`),
`GET /api/cad/{id}` (with the file), `POST /api/cad/{id}/ask` (`{"text"}`), `POST /api/cad/{id}/params`
(`{"values":{"name":"12"}}`, numbers or true/false only), `POST /api/cad/{id}/stop|keep`, `DELETE /api/cad/{id}`,
`GET /api/cad/{id}/model.stl|model.scad` (`?download=1` names the file).

## What is tested

Tested with OpenSCAD 2021.01 on Linux (a real node in the local test rig, inside the exec sandbox) and a stand-in
model that answers with scripted files. **Untested:** the quality of real local models at writing OpenSCAD,
OpenSCAD on Windows and macOS nodes, OpenSCAD versions other than 2021.01, and very large or slow renders
(a render stops after 5 minutes). Older OpenSCAD without `--export-format` falls back to ASCII STL.
