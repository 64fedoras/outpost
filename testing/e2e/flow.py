import asyncio, sys, json
from playwright.async_api import async_playwright
import os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "chat", exist_ok=True)
U = "https://127.0.0.1:18460"
scheme = sys.argv[1] if len(sys.argv) > 1 else "dark"

async def wait_status(pg, words, timeout=120):
    for _ in range(timeout * 2):
        t = await pg.inner_text("#tv-status")
        if any(w in t for w in words):
            return t
        await pg.wait_for_timeout(500)
    raise SystemExit(f"timed out waiting for {words}; status {t!r}")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch()
        pg = await b.new_page(ignore_https_errors=True, viewport={"width": 1360, "height": 900}, color_scheme=scheme)
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt"); await pg.wait_for_timeout(1500)
        await pg.screenshot(path=E + f"chat/1-new-{scheme}.png")
        await pg.fill("#nt-prompt", "Build a word counter CLI. ask me first about the details.")
        await pg.click("#nt-go")
        await wait_status(pg, ["Has questions"])
        await pg.wait_for_timeout(800)
        await pg.click(".opt >> text=Python"); await pg.click(".opt >> text=Case-insensitive"); await pg.click(".opt >> text=Top-N limit")
        await pg.screenshot(path=E + f"chat/2-questions-{scheme}.png")
        await pg.click("[data-act=task-answer]")
        await wait_status(pg, ["Plan ready"])
        await pg.wait_for_timeout(800)
        await pg.click(".plan-steps summary >> nth=1")
        await pg.screenshot(path=E + f"chat/3-plan-{scheme}.png")
        await pg.click("[data-act=task-approve]")
        await wait_status(pg, ["Working"])
        await pg.wait_for_timeout(2500)
        await pg.fill("#tm-text", "Keep the CLI output sorted alphabetically.")
        await pg.press("#tm-text", "Enter")
        await pg.wait_for_timeout(2500)
        await pg.screenshot(path=E + f"chat/4-working-{scheme}.png")
        await wait_status(pg, ["Done"], 180)
        await pg.wait_for_timeout(1500)
        await pg.screenshot(path=E + f"chat/5-done-{scheme}.png")
        await pg.fill("#tm-text", "Add a --top N option that prints only the N most common words.")
        await pg.click("#tm-send")
        await wait_status(pg, ["Plan ready"])
        await pg.wait_for_timeout(800)
        await pg.click("[data-act=task-approve]")
        await wait_status(pg, ["Done"], 180)
        await pg.wait_for_timeout(1500)
        await pg.screenshot(path=E + f"chat/6-followup-{scheme}.png", full_page=False)
        print("task url:", pg.url)
        print("page errors:", errors)
        await b.close()
asyncio.run(main())
