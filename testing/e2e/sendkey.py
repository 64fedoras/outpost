# The Chat send-key setting (Enter vs Ctrl+Enter) applies to every composer: Chat, the
# project conversation and the task follow-up box. Counts the POSTs each Enter makes.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
POST = "(a) => fetch(a[0], {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(a[1] || {})}).then(r => r.json())"

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#nt-prompt")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1600, "height": 900})
        pg = await ctx.new_page()
        errors = []
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await login(pg)
        pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Send key"}]))["id"]
        posts = []
        pg.on("request", lambda r: r.method == "POST" and posts.append(r.url))
        async def tries(url_part, sel, text):
            # returns (enter_sent, ctrl_enter_sent) for the box at sel
            out = []
            for key in ("Enter", "Control+Enter"):
                posts.clear()
                await pg.fill(sel, text)
                await pg.press(sel, key)
                await pg.wait_for_timeout(600)
                out.append(any(url_part in u for u in posts))
            return out
        for ctrl in (False, True):
            await pg.evaluate("(c) => c ? localStorage.setItem('outpost.chatSendKey','ctrl') : localStorage.removeItem('outpost.chatSendKey')", ctrl)
            want = [False, True] if ctrl else [True, True]
            await pg.goto(U + "/#/projects/" + pid); await pg.reload(); await pg.wait_for_selector("#pj-text")
            got = await tries("/chat", "#pj-text", "hello project")
            assert got == want, f"project box ctrl={ctrl}: {got} != {want}"
            await pg.goto(U + "/#/chat"); await pg.reload(); await pg.wait_for_selector("#chat-text")
            # a model may not be loaded on the rig: Enter still posts the message or an error, so look for any POST under /api/chats
            got = await tries("/api/chats", "#chat-text", "hello chat")
            assert got == want, f"chat box ctrl={ctrl}: {got} != {want}"
        # Lists continue on a newline in every box; an empty item ends the list (Shift+Enter is a newline in both modes).
        async def lists(sel):
            async def typed(parts):
                await pg.fill(sel, "")
                for t in parts:
                    await (pg.press(sel, "Shift+Enter") if t == "\n" else pg.keyboard.type(t))
                return await pg.input_value(sel)
            assert await typed(["1. a", "\n", "b", "\n"]) == "1. a\n2. b\n3. ", sel
            assert await typed(["1. a", "\n", "\n"]) == "1. a\n", sel
            assert await typed(["- a", "\n", "b"]) == "- a\n- b", sel
            assert await typed(["  * a", "\n"]) == "  * a\n  * ", sel
            assert await typed(["```", "\n", "- a", "\n"]) == "```\n- a\n", sel
            assert await typed(["plain", "\n"]) == "plain\n", sel
        await pg.evaluate("() => localStorage.removeItem('outpost.chatSendKey')")
        await pg.goto(U + "/#/projects/" + pid); await pg.reload(); await pg.wait_for_selector("#pj-text")
        await lists("#pj-text")
        await pg.goto(U + "/#/chat"); await pg.reload(); await pg.wait_for_selector("#chat-text")
        await lists("#chat-text")
        assert not errors, errors
        print("ok")
        await b.close()
asyncio.run(main())
