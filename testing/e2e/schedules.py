# Schedule dialog: every few hours, chosen days, monthly; the preview line and the saved pill.
# Screenshots go to $OUTPOST_E2E_DIR/schedules/.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
OUT = E + "schedules/"
os.makedirs(OUT, exist_ok=True)
U = "https://127.0.0.1:18460"
SCHEME = sys.argv[1] if len(sys.argv) > 1 else "dark"
POST = "(a) => fetch(a[0], {method: a[2] || 'POST', headers: {'Content-Type': 'application/json'}, body: a[1] ? JSON.stringify(a[1]) : undefined}).then(r => r.json())"
GET = "(u) => fetch(u).then(r => r.json())"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1360, "height": 860}, color_scheme=SCHEME)
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        for old in await pg.evaluate(GET, "/api/templates"):  # left over from an earlier run
            await pg.evaluate(POST, [f"/api/templates/{old['id']}", None, "DELETE"])
        tpl = await pg.evaluate(POST, ["/api/templates", {"name": "Nightly dependency bump", "request": {"prompt": "Bump dependencies and run the tests", "planner": "auto"}}])
        await pg.reload(); await pg.wait_for_selector(f"[data-act=tpl-sched][data-id='{tpl['id']}']")
        sched = f"[data-act=tpl-sched][data-id='{tpl['id']}']"

        async def save_and_check(want):
            await pg.click("#sched-go")
            await pg.wait_for_selector("#modal-sched.hidden", state="attached")
            await pg.wait_for_selector(f"#nt-saved .pill:has-text('{want}')")

        # every 6 hours from 02:15
        await pg.click(sched)
        await pg.select_option("#sched-every", "hours"); await pg.select_option("#sched-hours", "6"); await pg.fill("#sched-at", "02:15")
        assert "Every 6 hours from 02:15" in await pg.text_content("#sched-preview")
        assert await pg.is_visible("#sched-hours-wrap") and not await pg.is_visible("#sched-days-wrap")
        await pg.locator("#modal-sched .modal").screenshot(path=OUT + f"dialog-hours-{SCHEME}.png")
        await save_and_check("Every 6 hours from 02:15")
        t = (await pg.evaluate(GET, "/api/templates"))[0]
        assert t["schedule"] == {"every": "hours", "at": "02:15", "hours": 6}, t["schedule"]

        # weekdays at 07:00 (Mon to Fri are ticked by default)
        await pg.click(sched)
        await pg.select_option("#sched-every", "days"); await pg.fill("#sched-at", "07:00")
        assert "Weekdays at 07:00" in await pg.text_content("#sched-preview")
        await pg.locator("#modal-sched .modal").screenshot(path=OUT + f"dialog-days-{SCHEME}.png")
        await save_and_check("Weekdays at 07:00")
        t = (await pg.evaluate(GET, "/api/templates"))[0]
        assert t["schedule"]["days"] == [1, 2, 3, 4, 5], t["schedule"]

        # no day ticked is refused with a message
        await pg.click(sched)
        for c in await pg.locator("#sched-days-wrap input").all(): await c.uncheck()
        await pg.click("#sched-go")
        await pg.wait_for_selector("#sched-err:has-text('at least one day')")
        await pg.click("#modal-sched [data-close]")

        # monthly on the 31st
        await pg.click(sched)
        await pg.select_option("#sched-every", "monthly"); await pg.fill("#sched-mday", "31"); await pg.fill("#sched-at", "03:00")
        assert "last day" in await pg.text_content("#sched-preview")
        await pg.locator("#modal-sched .modal").screenshot(path=OUT + f"dialog-monthly-{SCHEME}.png")
        await save_and_check("Monthly on day 31 at 03:00")
        await pg.locator("#nt-saved").screenshot(path=OUT + f"saved-{SCHEME}.png")

        await pg.evaluate(POST, [f"/api/templates/{tpl['id']}", None, "DELETE"])

        # ---------- the Scheduled page ----------
        a = await pg.evaluate(POST, ["/api/templates", {"name": "Nightly dependency bump", "request": {"prompt": "Bump dependencies and run the tests", "planner": "auto", "node_label": "gpu"}, "schedule": {"every": "days", "at": "02:00", "days": [1, 2, 3, 4, 5]}}])
        b2 = await pg.evaluate(POST, ["/api/templates", {"name": "Monthly licence check", "request": {"prompt": "Check licence notices", "planner": "auto"}, "schedule": {"every": "monthly", "at": "06:00", "day": 1}}])
        c3 = await pg.evaluate(POST, ["/api/templates", {"name": "Add a CHANGELOG entry", "request": {"prompt": "Add a CHANGELOG entry for the last release", "planner": "auto"}}])
        await pg.goto(U + "/#/scheduled")
        row = lambda t: f"[data-sc-row='{t['id']}']"
        await pg.wait_for_selector(row(a))
        assert await pg.text_content("#badge-sched") == "2"
        assert "Weekdays at 02:00" in await pg.text_content(row(a))
        assert "UTC" in await pg.text_content("#sc-tz")
        # the unscheduled one is in its own group
        assert await pg.locator(f".sc-group + .sc-list {row(c3)}").count() == 1

        # Run now records a manual run; open the row to see it
        await pg.click(f"{row(b2)} [data-act=tpl-run]")
        await pg.wait_for_url("**/#/tasks/*")
        await pg.goto(U + "/#/scheduled"); await pg.wait_for_selector(row(b2))
        await pg.click(f"{row(b2)} .sc-main")
        await pg.wait_for_selector(f"{row(b2)}.open + .sc-detail .sc-hist")
        assert "Run now." in await pg.text_content(f"{row(b2)} + .sc-detail")
        assert await pg.locator(f"{row(b2)} .sc-runs i").count() == 1

        # Pause and Resume one
        await pg.click(f"{row(a)} [data-act=sc-pause]")
        await pg.wait_for_selector(f"{row(a)}.paused [data-act=sc-resume]")
        assert await pg.text_content("#badge-sched") == "1"
        await pg.screenshot(path=OUT + f"page-{SCHEME}.png", full_page=True)
        await pg.click(f"{row(a)} [data-act=sc-resume]")
        await pg.wait_for_selector(f"{row(a)} [data-act=sc-pause]")

        # Pause all: banner, badge, and Resume all
        await pg.click("#sc-pause-all")
        await pg.wait_for_selector(".sc-banner")
        assert await pg.get_attribute("#sc-pause-all", "aria-checked") == "true"
        assert "all paused" in await pg.text_content(row(a))
        await pg.screenshot(path=OUT + f"page-paused-all-{SCHEME}.png", full_page=True)
        await pg.click(".sc-banner [data-act=sc-pause-all]")
        await pg.wait_for_selector(".sc-banner", state="detached")

        # ⋯ menu: Edit request
        await pg.click(f"{row(a)} [data-act=sc-menu]")
        await pg.wait_for_selector("#sc-ctx:not(.hidden) [data-act=sc-edit]")
        await pg.locator("#sc-ctx").screenshot(path=OUT + f"menu-{SCHEME}.png")
        await pg.click("#sc-ctx [data-act=sc-edit]")
        await pg.fill("#tpl-edit-prompt", "Bump every dependency and run the tests")
        await pg.click("#tpl-edit-go")
        await pg.wait_for_selector("#modal-tpl-edit.hidden", state="attached")
        t = [x for x in await pg.evaluate(GET, "/api/templates") if x["id"] == a["id"]][0]
        assert t["request"]["prompt"] == "Bump every dependency and run the tests" and t["request"]["node_label"] == "gpu", t["request"]

        # Duplicate (unscheduled copy), Remove schedule, Escape closes the menu
        await pg.click(f"{row(a)} [data-act=sc-menu]"); await pg.click("#sc-ctx [data-act=sc-dup]")
        await pg.wait_for_selector(".sc-name:has-text('Nightly dependency bump (copy)')")
        await pg.click(f"{row(b2)} [data-act=sc-menu]"); await pg.keyboard.press("Escape")
        await pg.wait_for_selector("#sc-ctx.hidden", state="attached")
        await pg.click(f"{row(b2)} [data-act=sc-menu]"); await pg.click("#sc-ctx [data-act=sc-unsched]")
        await pg.click("#modal-ask:not(.hidden) #ask-yes")
        await pg.wait_for_selector(f".sc-group + .sc-list {row(b2)}")

        # phone
        await pg.set_viewport_size({"width": 390, "height": 844})
        await pg.reload(); await pg.wait_for_selector(row(a)); await asyncio.sleep(0.6)
        await pg.click(f"{row(a)} .sc-main"); await pg.wait_for_selector(f"{row(a)} + .sc-detail")
        await pg.screenshot(path=OUT + f"page-phone-{SCHEME}.png", full_page=True)
        await pg.set_viewport_size({"width": 1360, "height": 860})

        for x in await pg.evaluate(GET, "/api/templates"):
            await pg.evaluate(POST, [f"/api/templates/{x['id']}", None, "DELETE"])
        assert not errors, errors
        print("schedules ok")
        await b.close()

asyncio.run(main())
