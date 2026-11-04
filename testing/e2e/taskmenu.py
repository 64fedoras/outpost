# Sidebar task menu: right-click, the ⋯ button, keyboard (Shift+F10, F2, Delete),
# rename, pin, copy link and prompt, duplicate, delete, and the phone layout.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "menu", exist_ok=True)
U = "https://127.0.0.1:18460"
SCHEME = sys.argv[1] if len(sys.argv) > 1 else "dark"

def row(title):
    return f".side-task:has(.side-task-title:text-is('{title}'))"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1360, "height": 860}, color_scheme=SCHEME)
        await ctx.grant_permissions(["clipboard-read", "clipboard-write"], origin=U)
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        # network errors (401 before login, 404 for a deleted task) are expected; CSP violations are not
        pg.on("console", lambda m: m.type == "error" and "Failed to load resource" not in m.text and errors.append(m.text))
        pg.on("dialog", lambda d: asyncio.ensure_future(d.accept()))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        ids = []
        for prompt in ["Say hello in Python.", "Write a dice roller CLI."]:
            r = await pg.evaluate("(p) => fetch('/api/tasks', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({prompt: p})}).then(r => r.json())", prompt)
            assert r.get("id"), r
            ids.append(r["id"])
        await pg.wait_for_timeout(3000)
        first = pg.locator(f".side-task[data-task='{ids[0]}']")
        await first.wait_for()

        # right-click opens the menu next to the pointer
        await first.click(button="right")
        menu = pg.locator("#task-ctx")
        await menu.wait_for(state="visible")
        items = await menu.locator("button").all_inner_texts()
        assert items[0] == "Open in new tab" and any(i.startswith("Delete") for i in items), items
        assert await pg.evaluate("document.activeElement.dataset.ctx") == "open-tab"
        await pg.keyboard.press("ArrowDown")
        assert await pg.evaluate("document.activeElement.dataset.ctx") == "rename"
        await pg.keyboard.press("End")
        assert await pg.evaluate("document.activeElement.dataset.ctx") == "delete"
        await pg.screenshot(path=E + f"menu/{SCHEME}-open.png", clip={"x": 0, "y": 0, "width": 620, "height": 520})
        await pg.keyboard.press("Escape")
        await menu.wait_for(state="hidden")

        # copy link
        await first.click(button="right")
        await menu.locator("[data-ctx=copy-link]").click()
        await pg.wait_for_timeout(300)
        clip = await pg.evaluate("navigator.clipboard.readText()")
        assert clip.endswith("#/tasks/" + await first.get_attribute("data-task")), clip

        # copy prompt
        await first.click(button="right")
        await menu.locator("[data-ctx=copy-prompt]").click()
        await pg.wait_for_timeout(500)
        assert await pg.evaluate("navigator.clipboard.readText()") == "Say hello in Python."

        # rename from the menu; polling must not wipe the input
        tid = await first.get_attribute("data-task")
        await first.click(button="right")
        await menu.locator("[data-ctx=rename]").click()
        inp = pg.locator("#side-rename")
        await inp.wait_for()
        await pg.wait_for_timeout(2500)  # at least one poll
        assert await inp.is_visible(), "rename input lost to polling"
        await inp.fill("Renamed by menu")
        await pg.screenshot(path=E + f"menu/{SCHEME}-rename.png", clip={"x": 0, "y": 0, "width": 400, "height": 400})
        await pg.keyboard.press("Enter")
        await pg.wait_for_selector(row("Renamed by menu"))
        t = await pg.evaluate("(id) => fetch('/api/tasks/' + id).then(r => r.json())", tid)
        assert t["title"] == "Renamed by menu", t["title"]

        # F2 on a focused row, Escape cancels
        link = pg.locator(f".side-task[data-task='{tid}'] .side-task-link")
        await link.focus(); await pg.keyboard.press("F2")
        await pg.locator("#side-rename").fill("not saved")
        await pg.keyboard.press("Escape")
        await pg.wait_for_selector(row("Renamed by menu"))

        # Shift+F10 opens the menu by keyboard; pin moves the task to a Pinned group
        await link.focus(); await pg.keyboard.press("Shift+F10")
        await menu.wait_for(state="visible")
        await menu.locator("[data-ctx=pin]").click()
        await pg.wait_for_selector(".side-group:text-is('Pinned')")
        assert await pg.locator(".side-task").first.get_attribute("data-task") == tid
        await pg.wait_for_timeout(2500)
        assert await pg.locator(".side-task").first.get_attribute("data-task") == tid, "pin lost after a poll"
        await pg.screenshot(path=E + f"menu/{SCHEME}-pinned.png", clip={"x": 0, "y": 0, "width": 400, "height": 400})

        # ⋯ button opens the same menu; duplicate fills the new-task box
        other = pg.locator(f".side-task[data-task='{ids[1]}']")
        await other.locator(".side-task-link").click()  # from a task page, so duplicate has to switch pages
        await pg.wait_for_selector("#tv-title"); await pg.wait_for_timeout(500)
        await other.hover()
        await other.locator(".side-task-more").click()
        await menu.wait_for(state="visible")
        await menu.locator("[data-ctx=duplicate]").click()
        # (wait_for_function needs eval, which the CSP blocks; poll instead)
        for _ in range(20):
            if await pg.evaluate("document.getElementById('nt-prompt').value === 'Write a dice roller CLI.' && document.getElementById('nt-prompt').offsetParent !== null"):
                break
            await pg.wait_for_timeout(150)
        else:
            raise SystemExit("duplicate did not fill the new-task box: " + repr(await pg.evaluate("[location.hash, document.getElementById('nt-prompt').value, document.getElementById('toast').textContent]")))

        # delete from the menu (confirm accepted), then Delete key on a focused row
        before = await pg.locator(".side-task").count()
        oid = await other.get_attribute("data-task")
        await other.click(button="right")
        await menu.locator("[data-ctx=delete]").click()
        await pg.wait_for_selector(f".side-task[data-task='{oid}']", state="detached")
        assert await pg.locator(".side-task").count() == before - 1
        await link.focus(); await pg.keyboard.press("Delete")
        await pg.wait_for_selector(f".side-task[data-task='{tid}']", state="detached")
        st = await pg.evaluate("(id) => fetch('/api/tasks/' + id).then(r => r.status)", tid)
        assert st == 404, st

        # phone: the ⋯ button is always visible in the drawer
        for prompt in ["Make a todo list app."]:
            await pg.evaluate("(p) => fetch('/api/tasks', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({prompt: p})})", prompt)
        m = await b.new_context(ignore_https_errors=True, color_scheme=SCHEME, **p.devices["Pixel 7"])
        mp = await m.new_page()
        mp.on("pageerror", lambda e: errors.append(str(e)))
        await mp.goto(U + "/"); await mp.fill("#pw", open(E + "pw.txt").read().strip()); await mp.click("button[type=submit]")
        await mp.wait_for_selector("#nt-prompt"); await mp.wait_for_timeout(2500)
        await mp.click("#side-open"); await mp.wait_for_timeout(400)
        more = mp.locator(".side-task-more").first
        assert await more.is_visible(), "⋯ hidden on touch"
        await more.tap()
        await mp.locator("#task-ctx").wait_for(state="visible")
        await mp.screenshot(path=E + f"menu/{SCHEME}-phone.png")
        assert not errors, errors
        print("task menu ok:", SCHEME)
        await b.close()

asyncio.run(main())
