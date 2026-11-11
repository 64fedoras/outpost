# Edit the plan (before approval and after a finished run) and re-run it.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "chat", exist_ok=True)
U = "https://127.0.0.1:18460"

async def wait_status(pg, words, timeout=120):
    for _ in range(timeout * 2):
        t = await pg.inner_text("#tv-status")
        if any(w in t for w in words):
            return t
        await pg.wait_for_timeout(500)
    raise SystemExit(f"timed out waiting for {words}; status {t!r}")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        pg = await b.new_page(ignore_https_errors=True, viewport={"width": 1360, "height": 900})
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt"); await pg.wait_for_timeout(1500)
        await pg.fill("#nt-prompt", "Build a word counter CLI.")
        await pg.click("#nt-go")
        await wait_status(pg, ["Plan ready"])
        await pg.wait_for_timeout(800)
        # 1. edit the plan before approval: retitle, drop the last subtask, then approve from the editor
        await pg.click("[data-act=task-pe-open]")
        await pg.fill(".pe-title", "Edited counter")
        n = await pg.locator(".pe-sub").count()
        assert n >= 2, n
        await pg.locator("[data-act=task-pe-del]").last.click()
        assert await pg.locator(".pe-sub").count() == n - 1
        await pg.screenshot(path=E + "chat/plan-edit.png")
        await pg.click("[data-act=task-pe-run]")
        await wait_status(pg, ["Working"])
        assert await pg.locator("[data-act=task-pe-open]").count() == 0, "no plan edit while running"
        await wait_status(pg, ["Done"], 240)
        await pg.wait_for_timeout(1000)
        txt = await pg.inner_text("#thread")
        assert "Edited counter" in txt and "edited" in txt, txt[-500:]
        # 2. re-run the unchanged plan
        # The UI's own confirm dialog (confirm.js) replaced the native one: accept it as it appears.
        await pg.add_init_script("new MutationObserver(() => { const b = document.querySelector('#modal-ask:not(.hidden) #ask-yes'); if (b) b.click(); }).observe(document, {subtree: true, childList: true, attributes: true, attributeFilter: ['class']})")
        await pg.click("[data-act=task-rerun][data-mode=all]")
        await wait_status(pg, ["Working"])
        await wait_status(pg, ["Done"], 240)
        await pg.wait_for_timeout(1000)
        txt = await pg.inner_text("#thread")
        assert "approved the plan again" in txt, txt[-600:]
        await pg.screenshot(path=E + "chat/rerun-done.png")
        print("page errors:", errors)
        assert not errors
        await b.close()
asyncio.run(main())
