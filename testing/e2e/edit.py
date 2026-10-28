# Edit a sent message: before the work starts, and after a round has finished.
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

async def edit_first(pg, text):
    bubble = pg.locator(".bubble-row").first
    await bubble.hover()
    await bubble.locator("[data-act=task-msg-edit]").click()
    await pg.fill("#msg-edit-text", text)
    await pg.screenshot(path=E + "chat/edit-open.png")
    await pg.click("[data-act=task-msg-save]")

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
        await wait_status(pg, ["Plan ready", "Has questions"])
        await pg.wait_for_timeout(800)
        # 1. edit while the plan awaits approval
        await edit_first(pg, "Build a word counter CLI that ignores punctuation.")
        await wait_status(pg, ["Plan ready", "Has questions"])
        await pg.wait_for_timeout(1000)
        assert await pg.locator(".superseded").count() >= 1, "old branch should stay, dimmed"
        assert await pg.locator(".edited-mark").count() == 1, "edited marker"
        if await pg.locator("[data-act=task-answer]").count():
            await pg.click("[data-act=task-answer]"); await wait_status(pg, ["Plan ready"]); await pg.wait_for_timeout(800)
        await pg.click("[data-act=task-approve]")
        await wait_status(pg, ["Working"])
        assert await pg.locator("[data-act=task-msg-edit]").count() == 0, "no edit while running"
        await wait_status(pg, ["Done"], 180)
        await pg.wait_for_timeout(1000)
        # 2. edit the opening request after the round finished: files go back, new round starts
        await edit_first(pg, "Build a word counter CLI that prints counts as JSON.")
        await wait_status(pg, ["Plan ready", "Has questions"])
        await pg.wait_for_timeout(1000)
        txt = await pg.inner_text("#thread")
        assert "put back" in txt or "Edited request" in txt, txt[-600:]
        await pg.screenshot(path=E + "chat/edit-done.png")
        print("page errors:", errors)
        assert not errors
        await b.close()
asyncio.run(main())
