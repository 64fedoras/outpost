# Text selected on a project page survives the page's refreshes (relative times such as
# "19s ago" change on every poll), and the page catches up once the selection is gone.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
POST = "(a) => fetch(a[0], {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(a[1] || {})}).then(r => r.json())"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        pg = await (await b.new_context(ignore_https_errors=True, viewport={"width": 1600, "height": 900})).new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Keep selection"}]))["id"]
        await pg.evaluate(POST, ["/api/projects/" + pid + "/chat", {"text": "What is in this project? Please answer in a sentence."}])
        await pg.goto(U + "/#/projects/" + pid)
        await pg.wait_for_selector(".pj-ask .bubble")
        await pg.wait_for_timeout(1500)
        # select the bubble text, then force several refreshes with changed timestamps
        await pg.evaluate("() => { const r = document.createRange(); r.selectNodeContents(document.querySelector('.pj-ask .bubble')); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }")
        before = await pg.evaluate("() => getSelection().toString()")
        assert before.strip(), "nothing selected"
        for _ in range(4):
            await pg.evaluate("() => { document.querySelector('#pj-feed')._h = 'stale'; pjFetch(true); }")  # a refresh whose HTML differs
            await pg.wait_for_timeout(700)
        after = await pg.evaluate("() => getSelection().toString()")
        assert after == before, f"selection lost: {before!r} -> {after!r}"
        # clear the selection: the page catches up (the stale marker is replaced)
        await pg.evaluate("() => getSelection().removeAllRanges()")
        await pg.wait_for_timeout(600)
        assert await pg.evaluate("() => document.querySelector('#pj-feed')._h !== 'stale'"), "page did not catch up"
        assert not errors, errors
        print("ok")
        await b.close()
asyncio.run(main())
