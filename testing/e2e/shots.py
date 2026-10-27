import asyncio, os, sys
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "shots", exist_ok=True)
from playwright.async_api import async_playwright
U="https://127.0.0.1:18460"
async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch()
        pg = await b.new_page(ignore_https_errors=True, viewport={"width":1280,"height":900})
        errs = []
        pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#tile-nodes"); await pg.wait_for_timeout(2500)
        for name in ["home", "tasks", "tasks/task_001a170fd7fec0463ebaf4007", "nodes", "models", "updates", "settings"]:
            await pg.goto(U + "/#/" + name); await pg.wait_for_timeout(2500)
            await pg.screenshot(path="" + E + "shots/" + name.replace("/", "_") + ".png", full_page=True)
        await pg.goto(U + "/#/nodes"); await pg.wait_for_timeout(2500)
        await pg.click("[data-act=load]"); await pg.wait_for_timeout(500)
        await pg.screenshot(path="" + E + "shots/load.png")
        print("console errors:", errs or "none")
        await b.close()
asyncio.run(main())
