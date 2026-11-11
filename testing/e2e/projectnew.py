# Projects page: "New task here" opens the new-task page with that project picked.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1360, "height": 860})
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        r = await pg.evaluate("() => fetch('/api/projects', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name: 'e2e-newtask'})}).then(r => r.json())")
        pid = r.get("id") or r.get("project", {}).get("id")
        assert pid, r
        await pg.goto(U + "/#/projects")
        btn = pg.locator(f"a[data-act=project-use][data-id='{pid}'] button")
        await btn.wait_for()
        await btn.click()
        await pg.wait_for_function("() => location.hash === '#/new'", timeout=3000)
        await pg.wait_for_selector("section[data-page=new]:not(.hidden) #nt-prompt")
        assert await pg.input_value("#nt-project") == pid, await pg.input_value("#nt-project")
        await pg.evaluate(f"() => fetch('/api/projects/{pid}', {{method:'DELETE'}})")
        assert not errors, errors
        print("projectnew ok")
        await b.close()

asyncio.run(main())
