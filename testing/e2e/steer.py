# Send a note while the planner is working: the pass restarts with the note.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "chat", exist_ok=True)
U = "https://127.0.0.1:18460"

async def wait_status(pg, words, timeout=60):
    t = ""
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
        await pg.fill("#nt-prompt", "Build a word counter CLI, slow plan.")
        await pg.click("#nt-go")
        await wait_status(pg, ["Planning"])
        await pg.wait_for_timeout(1500)
        await pg.fill("#tm-text", "Keep the counts in SQLite.")
        await pg.screenshot(path=E + "chat/steer-typing.png")
        await pg.click("#tm-send")
        await pg.wait_for_timeout(1200)
        await pg.screenshot(path=E + "chat/steer-sent.png")
        await wait_status(pg, ["Plan ready"])
        await pg.screenshot(path=E + "chat/steer-plan.png")
        print("page errors:", errors or "none")
        await b.close()
asyncio.run(main())
