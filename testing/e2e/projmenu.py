# Project menu: right-click a project card, the ⋯ buttons, keyboard (Shift+F10, F2),
# Edit (name and instructions), Pin to sidebar, the pinned row's own menu, copy link,
# delete, and a long press on the phone. Screenshots go to $OUTPOST_E2E_DIR/projmenu/.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "projmenu", exist_ok=True)
U = "https://127.0.0.1:18460"
SCHEME = sys.argv[1] if len(sys.argv) > 1 else "dark"
POST = "(a) => fetch(a[0], {method: a[2] || 'POST', headers: {'Content-Type': 'application/json'}, body: a[1] ? JSON.stringify(a[1]) : undefined}).then(r => r.json())"

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#nt-prompt")

async def proj(pg, pid):
    return next(p for p in await pg.evaluate("() => fetch('/api/projects').then(r => r.json())") if p["id"] == pid)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1360, "height": 860}, color_scheme=SCHEME)
        await ctx.grant_permissions(["clipboard-read", "clipboard-write"], origin=U)
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        pg.on("console", lambda m: m.type == "error" and "Failed to load resource" not in m.text and errors.append(m.text))
        pg.on("dialog", lambda d: asyncio.ensure_future(d.accept()))
        await login(pg)
        pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Dice roller", "test_command": "pytest"}]))["id"]
        other = (await pg.evaluate(POST, ["/api/projects", {"name": "Scratch"}]))["id"]
        await pg.goto(U + "/#/projects")
        card = pg.locator(f".pj-card-wrap[data-proj='{pid}']")
        await card.wait_for()

        # right-click a card opens the menu at the pointer, first item focused
        await card.click(button="right")
        menu = pg.locator("#proj-ctx")
        await menu.wait_for(state="visible")
        items = await menu.locator("button").all_inner_texts()
        assert items[0] == "Open in new tab" and "Pin to sidebar" in items and any(i.startswith("Delete") for i in items), items
        assert await pg.evaluate("document.activeElement.dataset.pctx") == "open-tab"
        await pg.keyboard.press("ArrowDown")
        assert await pg.evaluate("document.activeElement.dataset.pctx") == "edit"
        await pg.screenshot(path=E + f"projmenu/{SCHEME}-card-menu.png", clip={"x": 0, "y": 0, "width": 900, "height": 520})
        await pg.keyboard.press("Escape")
        await menu.wait_for(state="hidden")

        # Edit: name and instructions in one dialog
        await card.click(button="right")
        await menu.locator("[data-pctx=edit]").click()
        await pg.wait_for_selector("#modal-pj-edit:not(.hidden)")
        assert await pg.evaluate("document.activeElement.id") == "pj-edit-instr"
        await pg.fill("#pj-edit-instr", "Use pytest. Keep Python 3.9 compatible.")
        await pg.screenshot(path=E + f"projmenu/{SCHEME}-edit.png")
        await pg.click("#modal-pj-edit [data-act=proj-edit-save]")
        await pg.wait_for_selector("#modal-pj-edit", state="hidden")
        assert (await proj(pg, pid))["instructions"] == "Use pytest. Keep Python 3.9 compatible."

        # Pin to sidebar: a pinned row appears under Projects
        await card.click(button="right")
        await menu.locator("[data-pctx=pin]").click()
        pin = pg.locator(f"#side-pins .side-pin[data-proj='{pid}']")
        await pin.wait_for()
        assert (await proj(pg, pid))["pinned"] is True
        await pg.wait_for_timeout(2500)  # a poll must keep it
        assert await pin.is_visible()

        assert not any(i.startswith("Rename") for i in items), items
        # F2 on the focused card opens the same Edit dialog; the sidebar follows
        await card.locator(".pj-card-link").focus()
        await pg.keyboard.press("F2")
        await pg.wait_for_selector("#modal-pj-edit:not(.hidden)")
        assert await pg.evaluate("document.activeElement.id") == "pj-edit-name"
        await pg.fill("#pj-edit-name", "Dice roller CLI")
        await pg.keyboard.press("Enter")
        await pg.wait_for_selector("#modal-pj-edit", state="hidden")
        await pg.wait_for_selector(f"#side-pins .side-pin[data-proj='{pid}'] .side-pin-name:text-is('Dice roller CLI')")

        # the pinned row's ⋯ opens the same menu, with Unpin; copy link
        await pin.hover()
        await pin.locator("[data-proj-menu]").click()
        await menu.wait_for(state="visible")
        assert "Unpin from sidebar" in await menu.locator("button").all_inner_texts()
        await pg.screenshot(path=E + f"projmenu/{SCHEME}-sidebar-menu.png", clip={"x": 0, "y": 0, "width": 620, "height": 520})
        await menu.locator("[data-pctx=copy-link]").click()
        await pg.wait_for_timeout(300)
        assert (await pg.evaluate("navigator.clipboard.readText()")).endswith("#/projects/" + pid)

        # the pinned row opens the project page, marked active; ⋯ in the page header and right-click on its title
        await pin.locator(".side-pin-link").click()
        await pg.wait_for_selector("section[data-page=project]:not(.hidden)")
        assert "active" in (await pin.get_attribute("class"))
        await pg.click("#pj-ctx-btn")
        await menu.wait_for(state="visible")
        await pg.screenshot(path=E + f"projmenu/{SCHEME}-page-menu.png")
        await pg.keyboard.press("Escape")
        await menu.wait_for(state="hidden")
        assert await pg.evaluate("document.activeElement.id") == "pj-ctx-btn"
        await pg.locator("#pj-name").click(button="right")
        await menu.wait_for(state="visible")
        await menu.locator("[data-pctx=settings]").click()
        await pg.wait_for_selector("#modal-pj-settings:not(.hidden)")
        await pg.keyboard.press("Escape")

        # Files no longer holds the instructions box
        await pg.click("#pj-files-btn")
        await pg.wait_for_selector("#modal-pj-files:not(.hidden)")
        assert await pg.locator("#modal-pj-files textarea").count() == 0
        await pg.keyboard.press("Escape")

        # Shift+F10 on a focused pinned row, then unpin
        await pin.locator(".side-pin-link").focus()
        await pg.keyboard.press("Shift+F10")
        await menu.wait_for(state="visible")
        await menu.locator("[data-pctx=pin]").click()
        await pin.wait_for(state="detached")
        assert (await proj(pg, pid))["pinned"] is False

        # Duplicate from the card menu: a new card "… (copy)" with the instructions
        await pg.goto(U + "/#/projects")
        await card.click(button="right")
        await menu.locator("[data-pctx=duplicate]").click()
        dupcard = pg.locator(".pj-card-wrap:has(.pj-card-name:text-is('▤ Dice roller CLI (copy)'))")
        await dupcard.wait_for()
        dup = await dupcard.get_attribute("data-proj")
        d = await proj(pg, dup)
        assert d["instructions"] == "Use pytest. Keep Python 3.9 compatible." and d["test_command"] == "pytest" and not d["pinned"], d
        await pg.evaluate(POST, [f"/api/projects/{dup}", None, "DELETE"])

        # a git project asks first: keep the repository or disconnect the copy. The rig cannot
        # clone, so the page is told this project has a repository.
        await pg.evaluate(f"() => {{ const p = projects.find((q) => q.id === '{pid}'); p.git_url = 'https://example.local/dice.git'; p.git_branch = 'outpost'; }}")
        await card.click(button="right")
        await menu.locator("[data-pctx=duplicate]").click()
        await pg.wait_for_selector("#modal-pj-dup:not(.hidden)")
        assert "example.local/dice.git" in await pg.inner_text("#pj-dup-body")
        await pg.screenshot(path=E + f"projmenu/{SCHEME}-dup-git.png")
        await pg.click("#pj-dup-body [data-dup=cancel]")
        await pg.wait_for_selector("#modal-pj-dup", state="hidden")
        await pg.wait_for_timeout(500)
        names = lambda: pg.evaluate("() => fetch('/api/projects').then(r => r.json()).then(l => l.map(p => p.name))")
        assert "Dice roller CLI (copy)" not in await names(), "cancel made a copy"
        await pg.evaluate(f"() => {{ const p = projects.find((q) => q.id === '{pid}'); p.git_url = 'https://example.local/dice.git'; }}")
        await card.click(button="right")
        await menu.locator("[data-pctx=duplicate]").click()
        await pg.click("#pj-dup-body [data-dup=detach]")
        await pg.wait_for_function("() => fetch('/api/projects').then(r => r.json()).then(l => l.some(p => p.name === 'Dice roller CLI (copy)'))")
        d = next(p for p in await pg.evaluate("() => fetch('/api/projects').then(r => r.json())") if p["name"] == "Dice roller CLI (copy)")
        dup = d["id"]
        assert d["git_url"] == "", d
        await pg.evaluate(POST, [f"/api/projects/{dup}", None, "DELETE"])

        # Delete from the menu (empty project)
        await pg.goto(U + "/#/projects")
        oc = pg.locator(f".pj-card-wrap[data-proj='{other}']")
        await oc.click(button="right")
        await menu.locator("[data-pctx=delete]").click()
        await oc.wait_for(state="detached")

        # phone: a long press on a pinned row opens the menu, and the press does not open the project
        await pg.evaluate(POST, [f"/api/projects/{pid}", {"pinned": True}])
        mctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True, color_scheme=SCHEME)
        mp = await mctx.new_page()
        mp.on("pageerror", lambda e: errors.append(str(e)))
        await login(mp)
        await mp.click("#side-open"); await mp.wait_for_timeout(600)  # the drawer slides in
        mpin = mp.locator(f"#side-pins .side-pin[data-proj='{pid}'] .side-pin-link")
        await mpin.wait_for()
        bb = await mpin.bounding_box()
        cdp = await mctx.new_cdp_session(mp)
        x, y = bb["x"] + 40, bb["y"] + bb["height"] / 2
        await cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y}]})
        await mp.wait_for_timeout(750)
        await cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
        await mp.locator("#proj-ctx").wait_for(state="visible")
        await mp.wait_for_timeout(300)
        assert not mp.url.endswith("#/projects/" + pid), mp.url
        await mp.screenshot(path=E + f"projmenu/{SCHEME}-phone-longpress.png")
        await mctx.close()

        await pg.evaluate(POST, [f"/api/projects/{pid}", None, "DELETE"])
        assert not errors, errors
        print("projmenu ok", SCHEME)
        await b.close()

asyncio.run(main())
