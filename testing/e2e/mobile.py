import asyncio
from playwright.async_api import async_playwright
import os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
os.makedirs(E + "chat", exist_ok=True)
U = "https://127.0.0.1:18460"
async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch()
        pg = await b.new_page(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", device_scale_factor=2)
        errors = []; pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt"); await pg.wait_for_timeout(1200)
        await pg.screenshot(path=E + "chat/m1-new.png")
        await pg.click("#side-open"); await pg.wait_for_timeout(400)
        await pg.screenshot(path=E + "chat/m2-side.png")
        await pg.click(".side-task >> nth=0"); await pg.wait_for_timeout(1500)
        await pg.screenshot(path=E + "chat/m3-thread.png")
        sw = await pg.evaluate("document.documentElement.scrollWidth")
        print("page scroll width at 390:", sw)
        pg2 = await b.new_page(ignore_https_errors=True, viewport={"width": 1360, "height": 800}, color_scheme="dark")
        await pg2.goto(U + "/#/nodes"); await pg2.wait_for_timeout(2000)
        await pg2.screenshot(path=E + "chat/nodes.png")
        print("errors:", errors)
        await b.close()
asyncio.run(main())
