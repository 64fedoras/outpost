# Documents in chat: attach a PDF and an XLSX with the 📎 button, see the chips in the
# composer, send, check the model got the text, the chips on the message (pages, sheets,
# words), the Text link, and a scanned PDF's warning (light, dark, phone).
import asyncio, io, os, zipfile
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/docs/"
F = E + "docs-files/"
os.makedirs(S, exist_ok=True); os.makedirs(F, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

def pdf(pages):
    b = io.BytesIO(); offs = []
    def obj(s):
        offs.append(b.tell()); b.write(f"{len(offs)} 0 obj\n{s}\nendobj\n".encode("latin-1"))
    b.write(b"%PDF-1.4\n")
    kids = " ".join(f"{4 + 2 * i} 0 R" for i in range(len(pages)))
    obj("<< /Type /Catalog /Pages 2 0 R >>")
    obj(f"<< /Type /Pages /Kids [{kids}] /Count {len(pages)} >>")
    obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
    for i, t in enumerate(pages):
        obj(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents {5 + 2 * i} 0 R >>")
        c = f"BT /F1 12 Tf 72 720 Td ({t}) Tj ET" if t else ""
        obj(f"<< /Length {len(c)} >>\nstream\n{c}\nendstream")
    x = b.tell()
    b.write(f"xref\n0 {len(offs) + 1}\n0000000000 65535 f \n".encode())
    for o in offs: b.write(f"{o:010d} 00000 n \n".encode())
    b.write(f"trailer\n<< /Size {len(offs) + 1} /Root 1 0 R >>\nstartxref\n{x}\n%%EOF\n".encode())
    return b.getvalue()

def xlsx():
    b = io.BytesIO()
    with zipfile.ZipFile(b, "w") as z:
        z.writestr("[Content_Types].xml", '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>')
        z.writestr("xl/workbook.xml", '<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Budget" sheetId="1" r:id="rId1"/></sheets></workbook>')
        z.writestr("xl/_rels/workbook.xml.rels", '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
        z.writestr("xl/sharedStrings.xml", '<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Item</t></si><si><t>Cost</t></si><si><t>Disks</t></si></sst>')
        z.writestr("xl/worksheets/sheet1.xml", '<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>240</v></c></row></sheetData></worksheet>')
    return b.getvalue()

open(F + "lab-plan.pdf", "wb").write(pdf(["Lab plan: three nodes share one model store", "Page two covers backups"]))
open(F + "budget.xlsx", "wb").write(xlsx())
open(F + "scan.pdf", "wb").write(pdf(["", ""]))

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=60000)
    await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 860}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        await pg.set_input_files("#chat-file", [F + "lab-plan.pdf", F + "budget.xlsx"])
        await pg.wait_for_selector("#chat-imgs .doc-chip >> nth=1")
        await pg.fill("#chat-text", "Summarise these for me.")
        await pg.locator(".chat-compose").screenshot(path=S + "composer-light.png")
        await pg.click("#chat-send")
        await pg.wait_for_selector(".chat-answer .chat-text", timeout=60000); await idle(pg)
        ans = await pg.inner_text(".chat-answer .chat-text")
        assert "lab-plan.pdf (PDF)" in ans and "Lab plan: three nodes" in ans and "budget.xlsx (XLSX)" in ans, ans
        chips = await pg.locator(".bubble-docs .doc-chip").all_inner_texts()
        assert "2 pages" in chips[0] and "1 sheet" in chips[1], chips
        assert await pg.locator("#chat-imgs .doc-chip").count() == 0
        href = await pg.get_attribute(".bubble-docs .doc-chip a >> nth=0", "href")
        r = await pg.request.get(U + href)
        assert r.ok and "[page 2]" in await r.text() and "backups" in await r.text(), await r.text()
        # A scanned PDF has no text layer: the chip says so.
        await pg.set_input_files("#chat-file", [F + "scan.pdf"])
        await pg.wait_for_selector("#chat-imgs .doc-chip")
        await pg.fill("#chat-text", "And this one?")
        await pg.click("#chat-send")
        await pg.wait_for_selector(".bubble-docs .doc-chip.warn", timeout=60000); await idle(pg)
        assert "scan" in (await pg.inner_text(".doc-chip.warn")).lower(), await pg.inner_text(".doc-chip.warn")
        await pg.screenshot(path=S + "chat-light.png")
        await pg.emulate_media(color_scheme="dark"); await pg.wait_for_timeout(500)
        await pg.screenshot(path=S + "chat-dark.png")
        await pg.set_viewport_size({"width": 390, "height": 800}); await pg.wait_for_timeout(600)
        await pg.screenshot(path=S + "chat-phone-dark.png")
        await b.close()
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
