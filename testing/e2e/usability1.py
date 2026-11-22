# Usability batch 1: plan card shows one decision (Adjust… reveals the rest), project composer asks on Enter,
# the New project dialog is name-only until "Start from a git repository", the result card has one primary
# download, Add node shows one OS at a time, the Chats panel starts closed. Shots go to run/shots/usability1/.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/usability1/"; os.makedirs(S, exist_ok=True)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="dark" if scheme != "light" else "light")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="attached")
            # New project dialog
            await pg.goto(U + "/#/projects"); await pg.wait_for_timeout(600)
            await pg.click("button[data-act=proj-new]")
            assert await pg.locator("#np-url").is_hidden(), "git fields hidden at first"
            await pg.screenshot(path=S + f"newproject-{scheme}.png")
            await pg.click("#np-git-show")
            assert await pg.locator("#np-url").is_visible(), "git fields shown on request"
            await pg.keyboard.press("Escape")
            await pg.evaluate("() => document.querySelectorAll('.overlay').forEach(o => o.classList.add('hidden'))")
            name = "usab1-" + scheme
            pid = await pg.evaluate("(n) => fetch('/api/projects', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name:n})}).then(r => r.json()).then(r => r.id || r.project.id)", name)
            await pg.goto(U + "/#/projects/" + pid); await pg.wait_for_selector("#pj-text")
            if scheme != "phone":
                assert await pg.locator("#pj-panel").is_hidden(), "Threads panel closed on an empty project"
            assert (await pg.inner_text("#pj-go")).strip() == "Ask"
            await pg.screenshot(path=S + f"project-empty-{scheme}.png")
            # Enter asks; Shift+Enter does not
            await pg.fill("#pj-text", "Write a python word counter in wordcount/core.py with a pytest test")
            await pg.keyboard.press("Shift+Enter")
            assert await pg.locator("#pj-feed .pj-ask").count() == 0, "Shift+Enter adds a line"
            await pg.fill("#pj-text", "What does this project do?"); await pg.keyboard.press("Enter")
            await pg.wait_for_selector("#pj-feed .pj-ask", timeout=10000)
            await pg.evaluate("(id) => fetch('/api/projects/' + id, {method:'DELETE'})", pid)
            # Add node: one OS at a time
            await pg.goto(U + "/#/nodes"); await pg.wait_for_selector("#btn-add"); await pg.click("#btn-add"); await pg.wait_for_timeout(600)
            await pg.click("[data-addos=win]")
            assert await pg.locator("#add-unix").is_hidden() and await pg.locator("#add-win").is_visible()
            await pg.click("[data-addos=unix]")
            assert await pg.locator("#add-win").is_hidden()
            await pg.screenshot(path=S + f"addnode-{scheme}.png")
            await pg.keyboard.press("Escape")
            assert not [e for e in errs if "401" not in e], errs
            await ctx.close()
        print("usability1 ok")
        await b.close()
asyncio.run(main())
