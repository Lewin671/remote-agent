#!/usr/bin/env python3
"""Synthetic PDF viewer fixtures; requires reportlab and pypdf only when regenerating.

The encrypted fixture's public test password is preview-fixture. No real document data.
"""
from pathlib import Path
from reportlab.pdfgen import canvas
from reportlab.lib.colors import HexColor, white
from pypdf import PdfReader, PdfWriter

ROOT = Path(__file__).resolve().parent


def document(name, count, mixed=False):
    out = canvas.Canvas(str(ROOT / name), pagesize=(420, 594), invariant=1, pageCompression=1)
    out.setTitle("PDF preview test fixture")
    out.setAuthor("Test fixture")
    for page in range(1, count + 1):
        width, height = (594, 420) if mixed and page == 2 else (420, 594)
        out.setPageSize((width, height))
        out.setFillColor(white)
        out.rect(0, 0, width, height, fill=1, stroke=0)
        out.setFillColor(HexColor("#2563EB"))
        out.rect(0, height - 12, width, 12, fill=1, stroke=0)
        out.setFillColor(HexColor("#172033"))
        out.setFont("Helvetica-Bold", 25)
        out.drawString(28, height - 62, "PDF preview")
        out.setFont("Helvetica", 12)
        out.drawString(28, height - 84, "A synthetic document for on-device verification")
        out.setStrokeColor(HexColor("#D6DEE8"))
        out.line(28, height - 104, width - 28, height - 104)
        out.setFont("Helvetica-Bold", 14)
        out.drawString(28, height - 134, f"Page {page:02d} of {count:02d}")
        out.setFont("Helvetica", 11)
        for row, label in enumerate(["Continuous vertical pages", "One shared document zoom", "Visible pages and one adjacent page", "Native Android PDF rendering"]):
            out.drawString(36, height - 162 - row * 22, f"{row + 1}.  {label}")
        y = 66
        for index, value in enumerate([0.72, 0.48, 0.9, 0.62]):
            x = 38 + index * (width - 70) / 4
            out.setFillColor(HexColor(["#2563EB", "#14B8A6", "#F59E0B", "#8B5CF6"][index]))
            out.roundRect(x, y, (width - 130) / 4, 90 * value, 3, fill=1, stroke=0)
        out.setFillColor(HexColor("#526174"))
        out.setFont("Helvetica", 9)
        out.drawString(28, 28, "Local test data - no account or private content")
        out.drawRightString(width - 28, 28, str(page))
        out.showPage()
    out.save()


document("preview.pdf", 3, mixed=True)
document("long-document.pdf", 60)
writer = PdfWriter()
writer.append(PdfReader(ROOT / "preview.pdf"))
writer.encrypt("preview-fixture", algorithm="RC4-128")
writer.write(ROOT / "encrypted.pdf")
PdfWriter().write(ROOT / "empty.pdf")
(ROOT / "damaged.pdf").write_bytes(b"%PDF-1.7\n1 0 obj\nThis is intentionally damaged test data.\n%%EOF\n")
