# Plan first with assumptions: a vague request gets a plan at once with an Assumed row; a changed chip turns the main
# button into Re-plan, which sends the change as a message and gets a revised plan; Other… takes the user's own value.
# The stub planner lists its choices when the request says "assume me". Shots go to run/shots/assume/.
import asyncio, json, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/assume/"; os.makedirs(S, exist_ok=True)
NEW = "(p) => fetch('/api/tasks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({prompt:p,planner:'claude'})}).then(r=>r.json()).then(r=>r.id||r.task.id)"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="light" if scheme == "light" else "dark")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="visible")
            tid = await pg.evaluate(NEW, "Write a small word counter, assume me " + scheme)
            await pg.goto(U + "/#/tasks/" + tid)
            await pg.wait_for_selector(".card-msg.plan.live .as-box", timeout=30000)
            assert await pg.locator(".card-msg .as-chip").count() == 4, "four assumptions"
            assert await pg.locator("text=A few questions before planning").count() == 0, "no question stop"
            assert (await pg.inner_text("[data-act=task-approve]")).strip() == "Approve and start"
            await pg.screenshot(path=S + f"plan-{scheme}.png")
            # change Storage
            await pg.select_option("select[data-asm=Storage]", "SQLite")
            await pg.wait_for_selector("[data-act=task-replan-asm]")
            assert (await pg.inner_text("[data-act=task-replan-asm]")).strip() == "Re-plan with SQLite"
            assert (await pg.inner_text("[data-act=task-approve]")).strip() == "Approve as it was"
            await pg.screenshot(path=S + f"changed-{scheme}.png")
            # back to the first value: normal buttons again
            await pg.select_option("select[data-asm=Storage]", "none (reads files)")
            await pg.wait_for_selector("[data-act=task-replan-asm]", state="detached")
            # Other…: the user's own value
            await pg.select_option("select[data-asm=Language]", "__other")
            await pg.wait_for_selector("#modal-ask:not(.hidden)")
            await pg.fill("#ask-input", "Rust"); await pg.click("#ask-yes")
            await pg.wait_for_selector("[data-act=task-replan-asm]")
            assert (await pg.inner_text("[data-act=task-replan-asm]")).strip() == "Re-plan with Rust"
            await pg.select_option("select[data-asm=Storage]", "SQLite")
            assert (await pg.inner_text("[data-act=task-replan-asm]")).strip() == "Re-plan with 2 changes"
            # Re-plan: the change goes to the planner as a message, the revised plan keeps the other assumptions
            await pg.click("[data-act=task-replan-asm]")
            await pg.wait_for_function("() => document.querySelectorAll('.card-msg.plan').length >= 2", timeout=30000)
            await pg.wait_for_selector(".card-msg.plan.live .as-box")
            assert await pg.locator("select[data-asm=Storage]").evaluate("e => e.value") == "SQLite", "storage kept"
            assert await pg.locator("select[data-asm=Language]").evaluate("e => e.value") == "Rust", "the typed value reached the planner"
            assert await pg.locator("select[data-asm=Interface]").evaluate("e => e.value") == "command line", "the other assumptions are kept"
            assert await pg.locator("[data-act=task-replan-asm]").count() == 0
            assert await pg.locator(".card-msg.plan:not(.live) .plan-assumed").count() >= 1, "older plan shows its assumptions read-only"
            # a clear request has no row
            tid2 = await pg.evaluate(NEW, "Write a small word counter " + scheme)
            await pg.goto(U + "/#/tasks/" + tid2)
            await pg.wait_for_selector(".card-msg.plan.live", timeout=30000)
            assert await pg.locator(".as-box").count() == 0, "no row when nothing was assumed"
            assert not errs, errs
            await ctx.close()
        await b.close()
    print("assume ok")
asyncio.run(main())
