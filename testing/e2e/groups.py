# Sidebar groups: By status / By group switch, New group, drag a task and a chat onto a group,
# right-click Move to group (fold-out), rename, colour, move, delete, the phone sheet.
# Screenshots go to $OUTPOST_E2E_DIR/groups/.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "groups", exist_ok=True)
U = "https://127.0.0.1:18460"
SCHEME = sys.argv[1] if len(sys.argv) > 1 else "dark"
POST = "(a) => fetch(a[0], {method: a[2] || 'POST', headers: {'Content-Type': 'application/json'}, body: a[1] ? JSON.stringify(a[1]) : undefined}).then(r => r.json())"
GET = "(u) => fetch(u).then(r => r.json())"
ACCEPT_ASK = "new MutationObserver(() => { const b = document.querySelector('#modal-ask:not(.hidden) #ask-yes'); if (b) b.click(); }).observe(document, {subtree: true, childList: true, attributes: true, attributeFilter: ['class']})"

async def until(pg, js, arg=None, tries=60):
    for _ in range(tries):
        if await pg.evaluate(js, arg): return
        await asyncio.sleep(0.25)
    raise AssertionError("timed out: " + js)

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#nt-prompt")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1360, "height": 860}, color_scheme=SCHEME)
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        pg.on("console", lambda m: m.type == "error" and "Failed to load resource" not in m.text and errors.append(m.text))
        # The UI's own confirm dialog (confirm.js) replaced the native one: accept it as it appears.
        await ctx.add_init_script(ACCEPT_ASK)
        await login(pg)
        t1 = (await pg.evaluate(POST, ["/api/tasks", {"prompt": "Write a hello world script in Python"}]))["id"]
        t2 = (await pg.evaluate(POST, ["/api/tasks", {"prompt": "Write a word counter in Python"}]))["id"]
        for t in (t1, t2):  # a task waiting on the plan is in Needs you; stop them so they are plain rows
            await until(pg, "(id) => fetch('/api/tasks/' + id).then(r => r.json()).then(t => t.status === 'awaiting_approval' || t.status === 'awaiting_answers')", t)
            await pg.evaluate(POST, [f"/api/tasks/{t}/cancel"])
        c1 = (await pg.evaluate(POST, ["/api/chats", {"text": "What is a GGUF file?"}]))["id"]
        await pg.reload(); await pg.wait_for_selector("#nt-prompt")
        await pg.wait_for_selector(f".side-chat[data-chat='{c1}']")  # chats are in the sidebar now

        # By status is the default view: no group sections, a chat row among the tasks
        assert await pg.locator(".gsec").count() == 0
        assert await pg.locator(f".side-task[data-task='{t1}']").count() == 1

        # New group from the bar
        await pg.click("[data-side-mode=group]")
        await pg.click("[data-group-add]")
        await pg.fill("#group-new", "Dice app"); await pg.press("#group-new", "Enter")
        await pg.wait_for_selector(".gsec[data-gsec^='grp_']")
        gs = await pg.evaluate(GET, "/api/groups"); assert [g["name"] for g in gs] == ["Dice app"], gs
        g1 = gs[0]["id"]
        # a duplicate name is refused with a toast and nothing is added
        await pg.click("[data-group-add]"); await pg.fill("#group-new", "dice APP"); await pg.press("#group-new", "Enter")
        await until(pg, "() => [...document.querySelectorAll('body *')].some(e => e.children.length === 0 && e.textContent.includes('already exists'))")
        assert len(await pg.evaluate(GET, "/api/groups")) == 1

        # Drag a task onto the group
        await pg.drag_and_drop(f".side-task[data-task='{t1}']", f".gsec[data-gsec='{g1}'] .gsec-h")
        await pg.wait_for_selector(f".gsec[data-gsec='{g1}'] .side-task[data-task='{t1}']")
        assert (await pg.evaluate(GET, f"/api/tasks/{t1}"))["group_id"] == g1
        # ... and a chat
        await pg.drag_and_drop(f".side-chat[data-chat='{c1}']", f".gsec[data-gsec='{g1}'] .gsec-h")
        await pg.wait_for_selector(f".gsec[data-gsec='{g1}'] .side-chat[data-chat='{c1}']")
        assert (await pg.evaluate(GET, "/api/chats"))[0]["group_id"] == g1

        # Right-click a task: Move to group fold-out, with the current group ticked
        await pg.click(f".side-task[data-task='{t2}']", button="right")
        menu = pg.locator("#task-ctx"); await menu.wait_for(state="visible")
        await menu.locator("[data-ctx=group]").click()
        sub = pg.locator("#group-sub"); await sub.wait_for(state="visible")
        assert "Dice app" in await sub.inner_text() and "Remove from group" not in await sub.inner_text()
        await pg.screenshot(path=E + f"groups/{SCHEME}-menu.png", clip={"x": 0, "y": 0, "width": 700, "height": 700})
        await sub.locator(f"[data-mv='{g1}']").click()
        await pg.wait_for_selector(f".gsec[data-gsec='{g1}'] .side-task[data-task='{t2}']")
        # ... and take it out again from the fold-out
        await pg.click(f".side-task[data-task='{t2}']", button="right")
        await menu.locator("[data-ctx=group]").click(); await sub.wait_for(state="visible")
        await sub.locator("[data-mv='']").click()
        await pg.wait_for_selector(f".gsec[data-gsec=''] .side-task[data-task='{t2}']")

        # Right-click a chat: its menu has Move to group too; "New group…" creates and moves
        await pg.click(f".side-chat[data-chat='{c1}']", button="right")
        cm = pg.locator("#group-ctx"); await cm.wait_for(state="visible")
        await cm.locator("[data-gctx=group]").click(); await sub.wait_for(state="visible")
        await sub.locator("[data-mv-new]").click()
        await pg.fill("#group-new", "Home lab"); await pg.press("#group-new", "Enter")
        await until(pg, "() => document.querySelectorAll('.gsec[data-gsec^=\"grp_\"]').length === 2")
        gs = await pg.evaluate(GET, "/api/groups"); g2 = next(g["id"] for g in gs if g["name"] == "Home lab")
        assert (await pg.evaluate(GET, "/api/chats"))[0]["group_id"] == g2

        # Group menu: rename, colour, move up, collapse, delete
        await pg.click(f".gsec-h[data-gtoggle='{g2}']", button="right")
        await cm.wait_for(state="visible")
        await cm.locator("[data-gctx=rename]").click()
        await pg.fill("#group-rename", "Lab"); await pg.press("#group-rename", "Enter")
        await until(pg, "(id) => document.querySelector(`[data-gtoggle='${id}'] .gname`)?.textContent === 'Lab'", g2)
        await pg.click(f".gsec-h[data-gtoggle='{g2}']", button="right")
        await cm.locator("[data-gcolor=red]").click()
        await pg.click(f".gsec-h[data-gtoggle='{g2}']", button="right")
        await cm.locator("[data-gctx=up]").click()
        await until(pg, "(id) => document.querySelector('.gsec[data-gsec^=\"grp_\"]').dataset.gsec === id", g2)
        gs = await pg.evaluate(GET, "/api/groups"); assert gs[0]["id"] == g2 and gs[0]["color"] == "red" and gs[0]["name"] == "Lab", gs
        await pg.screenshot(path=E + f"groups/{SCHEME}-groups.png", clip={"x": 0, "y": 0, "width": 420, "height": 860})
        await pg.click(f".gsec-h[data-gtoggle='{g1}']")
        assert await pg.locator(f".gsec[data-gsec='{g1}'].closed").count() == 1
        await pg.reload(); await pg.wait_for_selector(".gsec")
        assert await pg.locator(f".gsec[data-gsec='{g1}'].closed").count() == 1  # remembered
        await pg.click(f".gsec-h[data-gtoggle='{g1}']")
        await pg.click(f".gsec-h[data-gtoggle='{g1}']", button="right")
        await cm.locator("[data-gctx=delete]").click()
        await pg.wait_for_selector(f".gsec[data-gsec='{g1}']", state="detached")
        assert (await pg.evaluate(GET, f"/api/tasks/{t1}")).get("group_id", "") == ""
        assert await pg.locator(f".gsec[data-gsec=''] .side-task[data-task='{t1}']").count() == 1  # kept, ungrouped

        # Phone: long-press is covered by projmenu; here the fold-out becomes a bottom sheet
        m = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme=SCHEME, is_mobile=True, has_touch=True, device_scale_factor=2)
        mp = await m.new_page(); mp.on("pageerror", lambda e: errors.append(str(e))); mp.on("dialog", lambda d: asyncio.ensure_future(d.accept()))
        await login(mp)
        await mp.click("#side-open"); await mp.click("[data-side-mode=group]"); await mp.wait_for_selector(".gsec")
        await mp.click(f".side-task[data-task='{t2}'] .side-task-more")
        await mp.locator("#task-ctx [data-ctx=group]").tap()
        await mp.locator("#group-sub").wait_for(state="visible")
        box = await mp.locator("#group-sub").bounding_box(); assert box["y"] + box["height"] >= 843 and box["width"] >= 389, box
        await mp.screenshot(path=E + f"groups/{SCHEME}-phone-sheet.png")

        assert not errors, errors
        await b.close()
        print("groups ok")

asyncio.run(main())
