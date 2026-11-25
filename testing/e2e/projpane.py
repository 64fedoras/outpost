# Project pane: a task opens inside the Threads pane (address, back, full page, the
# composer kept), the pane's own task width, and the Context tab (instructions, a
# memory note added and forgotten, Stash upload and removal, folding). Desktop light and
# dark; on a phone a task link opens the task page. Shots in $OUTPOST_E2E_DIR/shots/projpane/.
import asyncio, os, re
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
OUT = E + "shots/projpane/"
os.makedirs(OUT, exist_ok=True)
U = "https://127.0.0.1:18460"
POST = "(a) => fetch(a[0], {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(a[1] || {})}).then(r => r.json())"
ACCEPT = "new MutationObserver(() => { const b = document.querySelector('#modal-ask:not(.hidden) #ask-yes'); if (b && ![...document.querySelectorAll('#modal-ask input, #modal-ask textarea')].some((i) => i.offsetParent)) b.click(); }).observe(document, {subtree: true, childList: true, attributes: true, attributeFilter: ['class']})"

async def until(pg, expr, timeout=15000):
    # page.wait_for_function evaluates a string, which the UI's CSP forbids; poll instead
    for _ in range(timeout // 100):
        if await pg.evaluate(f"() => !!({expr})"):
            return
        await pg.wait_for_timeout(100)
    raise AssertionError("timed out: " + expr)

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#nt-prompt")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        errors, first = [], None
        for scheme in ("light", "dark"):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1600, "height": 900}, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errors.append(str(e)))
            await pg.add_init_script(ACCEPT)
            await login(pg)
            pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Dice roller " + scheme}]))["id"]
            first = first or pid
            tid = (await pg.evaluate(POST, ["/api/tasks", {"prompt": "Write a dice roller CLI in Python with a pytest test", "project": pid}]))["id"]
            await pg.goto(U + "/#/projects/" + pid)
            await pg.wait_for_selector("#pj-name")
            if not await pg.locator("#pj-wrap.panel-open").count():
                await pg.click("#pj-threads-btn")
            if await pg.locator("#pj-tab-context.on").count():
                await pg.click("#pj-tab-threads")
            await pg.wait_for_selector("#pj-threads .pj-row")
            await pg.fill("#pj-text", "a draft that must survive")

            # a click on the row opens the task inside the pane
            await pg.click("#pj-threads .pj-row-main")
            await until(pg, f"location.hash === '#/projects/{pid}/tasks/{tid}'")
            await pg.wait_for_selector("#pj-panel section.page-task.in-pane #thread .card-msg, #pj-panel section.page-task.in-pane #thread > *")
            assert await pg.locator("#pj-panel #tm-text").is_visible()
            assert not await pg.locator("#pj-threads").is_visible() and not await pg.locator("#pj-tabs").is_visible()
            assert await pg.locator("#pj-pane-back").is_visible() and await pg.locator("#pj-pane-full").get_attribute("href") == "#/tasks/" + tid
            w = await pg.evaluate("document.querySelector('#pj-panel').getBoundingClientRect().width")
            assert abs(w - 640) < 2, w
            await pg.wait_for_selector("#pj-panel [data-act=task-approve]")
            assert await pg.input_value("#pj-text") == "a draft that must survive"
            await pg.wait_for_timeout(500)
            await pg.screenshot(path=OUT + f"task-in-pane-{scheme}.png")
            # back to the list
            await pg.click("#pj-pane-back")
            await pg.wait_for_selector("#pj-threads .pj-row")
            assert await pg.locator("#pj-panel section.page-task").count() == 0
            assert abs(await pg.evaluate("document.querySelector('#pj-panel').getBoundingClientRect().width") - 380) < 2
            assert await pg.input_value("#pj-text") == "a draft that must survive"
            # a card in the conversation opens in the pane too; ↗ opens the full page
            await pg.click("#pj-feed a[href='#/tasks/" + tid + "']")
            await pg.wait_for_selector("#pj-panel section.page-task.in-pane")
            await pg.click("#pj-pane-full")
            await until(pg, f"location.hash === '#/tasks/{tid}'")
            await pg.wait_for_selector("section.page-task:not(.hidden):not(.in-pane) [data-act=task-approve]")
            assert await pg.evaluate("document.querySelector('section.page-task').parentElement.id") != "pj-panel"

            # Context tab
            await pg.goto(U + "/#/projects/" + pid); await pg.wait_for_selector("#pj-tab-context")
            if not await pg.locator("#pj-wrap.panel-open").count():
                await pg.click("#pj-threads-btn")
            await pg.click("#pj-tab-context")
            await pg.wait_for_selector("#pj-context .pj-sec.instr")
            assert "No instructions yet" in await pg.inner_text("#pj-context .pj-sec.instr")
            await pg.click("#pj-context .pj-sec.instr [data-act=proj-ctx-instr]")
            await pg.wait_for_selector("#modal-pj-edit:not(.hidden)")
            await pg.fill("#pj-edit-instr", "Keep it Python 3.9 compatible. Use pytest.")
            await pg.click("#modal-pj-edit [data-act=proj-edit-save]")
            await pg.wait_for_selector("#modal-pj-edit", state="hidden")
            await pg.wait_for_selector("#pj-context .pj-sec.instr .pj-cp")
            # memory note through Add context
            await pg.click("#pj-context .pj-addctx summary")
            await pg.click("#pj-context .pj-addctx [data-act=proj-ctx-remember]")
            await pg.wait_for_selector("#modal-ask:not(.hidden) input, #modal-ask:not(.hidden) textarea")
            await pg.fill("#modal-ask:not(.hidden) input, #modal-ask:not(.hidden) textarea", "The dice app rolls d20 by default.")
            await pg.click("#modal-ask #ask-yes")
            await pg.wait_for_selector("#pj-context .pj-crow.mem:has-text('rolls d20')")
            mem = await pg.evaluate("() => fetch('/api/memory').then(r => r.json())")
            assert any(m["project"] == pid and "d20" in m["text"] for m in mem["memories"]), mem
            # Stash upload
            async with pg.expect_file_chooser() as fc:
                await pg.click("#pj-context .pj-sec.stash [data-act=proj-ctx-upload]")
            chooser = await fc.value
            await chooser.set_files([{"name": "rules.md", "mimeType": "text/markdown", "buffer": b"# Rules\nRoll NdM and add the modifier."},
                                     {"name": "notes.txt", "mimeType": "text/plain", "buffer": b"The CLI takes 2d6+1 style arguments."}])
            await until(pg, "document.querySelectorAll('#pj-context .pj-crow.res').length === 2", 20000)
            await pg.wait_for_timeout(800)
            await pg.screenshot(path=OUT + f"context-{scheme}.png")
            # fold a section, kept after reload
            await pg.click("#pj-context [data-act=proj-ctx-fold][data-key=files]")
            await pg.reload(); await pg.wait_for_selector("#pj-context .pj-sec.files")
            assert await pg.locator("#pj-tab-context.on").count() == 1
            assert await pg.locator("#pj-context .pj-sec.files .pj-cnote").count() == 0
            await pg.click("#pj-context [data-act=proj-ctx-fold][data-key=files]")
            # forget the memory and remove a document
            await pg.wait_for_selector("#pj-context .pj-crow.mem")
            await pg.hover("#pj-context .pj-crow.mem")
            await pg.click("#pj-context .pj-crow.mem [data-act=proj-ctx-forget]")
            await until(pg, "!document.querySelector('#pj-context .pj-crow.mem')")
            await pg.hover("#pj-context .pj-crow.res >> nth=0")
            await pg.click("#pj-context .pj-crow.res >> nth=0 >> [data-act=proj-ctx-unstash]")
            await until(pg, "document.querySelectorAll('#pj-context .pj-crow.res').length === 1")
            # the Threads pane shows the Stash document as an artifact
            await pg.click("#pj-tab-threads")
            await pg.wait_for_selector("#pj-threads .pj-acard.k-resource")
            await ctx.close()
        # phone: a task link opens the task page, not a pane
        ctx = await b.new_context(ignore_https_errors=True, **p.devices["iPhone 13"], color_scheme="dark")
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await login(pg)
        await pg.goto(U + "/#/projects/" + first); await pg.wait_for_selector("#pj-name")
        await pg.click("#pj-threads-btn")
        if await pg.locator("#pj-tab-context.on").count():
            await pg.click("#pj-tab-threads")
        await pg.click("#pj-threads .pj-row-main")
        await until(pg, "/^#\\/tasks\\//.test(location.hash)")
        await pg.goto(U + "/#/projects/" + first); await pg.wait_for_selector("#pj-name")
        await pg.click("#pj-threads-btn"); await pg.click("#pj-tab-context")
        await pg.wait_for_selector("#pj-context .pj-sec.stash")
        await pg.wait_for_timeout(500)
        await pg.screenshot(path=OUT + "context-phone.png")
        await ctx.close()
        await b.close()
        assert not errors, errors
        print("projpane: ok")

asyncio.run(main())
