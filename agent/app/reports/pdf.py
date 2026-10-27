"""Delivery reports as PDF: summary tiles, charts, and a table.

Drawn with reportlab (vector charts, so they stay sharp in print). The
design follows common logistics dashboards: outcome mix, volume over time,
per-truck performance and the on-time rate, each answering one question.

Colour rules (from the house data-viz method):
- Outcomes are status, so they use the reserved status colours, always
  with a label: completed = good green, failed = critical red, cancelled =
  neutral grey, still open = series blue.
- Two series over time (created, completed) take the first two categorical
  slots, in that fixed order.
- A two-part split (on time vs late) is a number, not a pie: a stat tile
  with a thin bar under it.
- Hairline grid and axes, thin bars with a 2 pt gap, labels in ink colours,
  never in a series colour."""

import io
from dataclasses import dataclass
from datetime import datetime

from reportlab.graphics.shapes import Drawing, Line, Rect, String, Wedge
from reportlab.lib.colors import HexColor
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle
from reportlab.lib.units import mm
from reportlab.platypus import (
    Flowable,
    KeepTogether,
    LongTable,
    Paragraph,
    SimpleDocTemplate,
    Spacer,
    TableStyle,
)

# ---- palette (light surface; print) ----
SURFACE = HexColor("#fcfcfb")
INK = HexColor("#0b0b0b")
INK_2 = HexColor("#52514e")
MUTED = HexColor("#898781")
GRID = HexColor("#e1e0d9")
AXIS = HexColor("#c3c2b7")
TILE = HexColor("#f4f3ef")

SERIES_1 = HexColor("#2a78d6")  # blue: created / still open
SERIES_2 = HexColor("#eb6834")  # orange: completed (in the daily series)
GOOD = HexColor("#0ca30c")
CRITICAL = HexColor("#d03b3b")
SERIOUS = HexColor("#ec835a")
NEUTRAL = HexColor("#a9a8a1")

OUTCOMES = [  # (key, label, colour) in a fixed order
    ("completed", "Completed", GOOD),
    ("open", "Still open", SERIES_1),
    ("cancelled", "Cancelled", NEUTRAL),
    ("failed", "Failed", CRITICAL),
]

CHARTS = ("status", "daily", "by_truck", "on_time")

FONT, BOLD = "Helvetica", "Helvetica-Bold"
PAGE_W, PAGE_H = A4
MARGIN = 16 * mm
CONTENT_W = PAGE_W - 2 * MARGIN


@dataclass
class ReportMeta:
    title: str
    company: str
    period_label: str
    timezone: str
    made_at: datetime


# ---- helpers ----

def _open_count(by_status: dict) -> int:
    return sum(by_status.get(k, 0) for k in ("pending", "assigned", "in_transit"))


def _pct(n: int, d: int) -> str:
    return f"{round(100 * n / d)}%" if d else "–"


def _nice_max(v: float) -> tuple[float, float]:
    """An axis top and step: 1, 2 or 5 times a power of ten, 4-6 ticks."""
    if v <= 0:
        return 4, 1
    import math

    raw = v / 4
    mag = 10 ** math.floor(math.log10(raw))
    for m in (1, 2, 5, 10):
        step = m * mag
        if step >= raw:
            break
    step = max(1, step) if v >= 4 else max(step, 1)
    top = step * math.ceil(v / step)
    return top, step


def _text(d: Drawing, x, y, s, size=8, color=MUTED, font=FONT, anchor="start"):
    d.add(String(x, y, s, fontName=font, fontSize=size, fillColor=color, textAnchor=anchor))


def _legend(d: Drawing, x, y, items, size=8):
    """A row of swatch + label (+ value). Identity never rests on colour alone."""
    from reportlab.pdfbase.pdfmetrics import stringWidth

    for label, color in items:
        d.add(Rect(x, y - 1, 8, 8, rx=2, ry=2, fillColor=color, strokeColor=None))
        _text(d, x + 12, y, label, size, INK_2)
        x += 12 + stringWidth(label, FONT, size) + 14


def _title(d: Drawing, x, y, title, subtitle=""):
    _text(d, x, y, title, 11, INK, BOLD)
    if subtitle:
        _text(d, x, y - 13, subtitle, 8, MUTED)


# ---- the parts ----

class Tiles(Flowable):
    """The headline numbers, one tile each."""

    def __init__(self, report: dict):
        super().__init__()
        bs = report["by_status"]
        total = report["total"]
        ot = report["on_time"]
        due_done = ot["on_time"] + ot["late"]
        avg = report.get("avg_complete_min")
        avg_s = "–" if avg is None else (f"{avg / 60:.1f} h" if avg >= 90 else f"{round(avg)} min")
        self.tiles = [
            ("Deliveries", str(total), "created in the period"),
            ("Completed", str(bs.get("completed", 0)), _pct(bs.get("completed", 0), total) + " of all"),
            ("Cancelled", str(bs.get("cancelled", 0)), _pct(bs.get("cancelled", 0), total) + " of all"),
            ("Failed", str(bs.get("failed", 0)), _pct(bs.get("failed", 0), total) + " of all"),
            ("On time", _pct(ot["on_time"], due_done), f"of {due_done} with a due time"),
            ("Avg time", avg_s, "created to delivered"),
        ]
        self.width, self.height = CONTENT_W, 22 * mm

    def wrap(self, aw, ah):
        return self.width, self.height

    def draw(self):
        c = self.canv
        n = len(self.tiles)
        gap = 2.5 * mm
        w = (self.width - gap * (n - 1)) / n
        for i, (label, value, note) in enumerate(self.tiles):
            x = i * (w + gap)
            c.setFillColor(TILE)
            c.roundRect(x, 0, w, self.height, 4, stroke=0, fill=1)
            c.setFillColor(MUTED)
            c.setFont(FONT, 7.5)
            c.drawString(x + 3 * mm, self.height - 5.5 * mm, label.upper())
            c.setFillColor(INK)
            c.setFont(BOLD, 17)
            c.drawString(x + 3 * mm, self.height - 13.5 * mm, value)
            c.setFillColor(MUTED)
            c.setFont(FONT, 6.5)
            c.drawString(x + 3 * mm, 3 * mm, note)


def status_chart(report: dict) -> Drawing:
    """Outcome mix: a donut (part of a whole, at most four parts) with the
    completion rate in the middle and a labelled legend with counts."""
    w, h = CONTENT_W, 62 * mm
    d = Drawing(w, h)
    bs = report["by_status"]
    total = report["total"]
    values = {"completed": bs.get("completed", 0), "open": _open_count(bs),
              "cancelled": bs.get("cancelled", 0), "failed": bs.get("failed", 0)}
    _title(d, 0, h - 11, "Outcomes", "What happened to the deliveries created in the period")
    cx, cy, r, inner = 40 * mm, (h - 18) / 2, 21 * mm, 13.5 * mm
    if total == 0:
        d.add(Wedge(cx, cy, r, 0, 359.99, radius1=inner, fillColor=GRID, strokeColor=None))
    else:
        angle = 90.0  # start at 12 o'clock, clockwise
        for key, _label, color in OUTCOMES:
            v = values[key]
            if not v:
                continue
            sweep = 360.0 * v / total
            start = angle - sweep
            # A 1.5 degree surface gap between parts (none for a full ring).
            gap = 0.75 if sweep < 359 else 0
            d.add(Wedge(cx, cy, r, start + gap, angle - gap, radius1=inner, fillColor=color,
                        strokeColor=None))
            angle = start
    _text(d, cx, cy + 1, _pct(values["completed"], total), 18, INK, BOLD, "middle")
    _text(d, cx, cy - 11, "completed", 8, MUTED, FONT, "middle")

    # Legend as a small table: swatch, label, count, share.
    x0, y = 82 * mm, cy + 22
    for key, label, color in OUTCOMES:
        v = values[key]
        d.add(Rect(x0, y - 1, 9, 9, rx=2, ry=2, fillColor=color, strokeColor=None))
        _text(d, x0 + 15, y, label, 9.5, INK)
        _text(d, x0 + 70 * mm, y, str(v), 9.5, INK, BOLD, "end")
        _text(d, x0 + 88 * mm, y, _pct(v, total), 9.5, MUTED, FONT, "end")
        d.add(Line(x0, y - 6, x0 + 88 * mm, y - 6, strokeColor=GRID, strokeWidth=0.5))
        y -= 17
    return d


def daily_chart(report: dict) -> Drawing:
    """Volume over time: created and completed per day, grouped bars."""
    days = report["by_day"]
    w, h = CONTENT_W, 70 * mm
    d = Drawing(w, h)
    _title(d, 0, h - 11, "Deliveries per day", "Created and completed, by day")
    _legend(d, w - 62 * mm, h - 11, [("Created", SERIES_1), ("Completed", SERIES_2)])
    left, bottom, top = 10 * mm, 12 * mm, h - 24 * mm
    plot_w, plot_h = w - left, top - bottom
    vmax, step = _nice_max(max([max(x["created"], x["completed"]) for x in days] + [0]))
    # Grid and y ticks.
    v = 0.0
    while v <= vmax + 1e-9:
        y = bottom + plot_h * v / vmax
        d.add(Line(left, y, w, y, strokeColor=AXIS if v == 0 else GRID, strokeWidth=0.6 if v == 0 else 0.4))
        _text(d, left - 4, y - 3, f"{int(v)}", 7, MUTED, FONT, "end")
        v += step
    n = max(1, len(days))
    slot = plot_w / n
    bar = max(1.2, min(9.0, (slot - 4) / 2 - 1))
    label_every = max(1, round(n / 12))
    for i, day in enumerate(days):
        x = left + i * slot + slot / 2
        for j, (key, color) in enumerate((("created", SERIES_1), ("completed", SERIES_2))):
            val = day[key]
            if val:
                bh = plot_h * val / vmax
                bx = x - bar - 1 if j == 0 else x + 1  # 2 pt gap between the pair
                d.add(Rect(bx, bottom, bar, bh, rx=min(2, bar / 2), ry=min(2, bar / 2),
                           fillColor=color, strokeColor=None))
        if i % label_every == 0:
            dt = datetime.strptime(day["date"], "%Y-%m-%d")
            label = dt.strftime("%a %d") if n <= 8 else dt.strftime("%d %b")
            _text(d, x, bottom - 10, label, 7, MUTED, FONT, "middle")
    return d


def truck_chart(report: dict, limit: int = 12) -> Drawing:
    """Per truck: one horizontal stacked bar each (completed, failed,
    cancelled, still open), busiest first; the rest folded into Other."""
    trucks = list(report["by_truck"])
    if len(trucks) > limit:
        rest = trucks[limit - 1:]
        trucks = trucks[: limit - 1] + [{
            "plate": f"Other ({len(rest)})",
            **{k: sum(t[k] for t in rest) for k in ("completed", "failed", "cancelled", "open")},
        }]
    parts = [("completed", "Completed", GOOD), ("failed", "Failed", CRITICAL),
             ("cancelled", "Cancelled", NEUTRAL), ("open", "Still open", SERIES_1)]
    row_h = 7.5 * mm
    h = 32 * mm + row_h * max(1, len(trucks))
    w = CONTENT_W
    d = Drawing(w, h)
    _title(d, 0, h - 11, "Per truck", "Deliveries each truck had, by outcome")
    _legend(d, 0, h - 40, [(lbl, c) for _, lbl, c in parts])
    if not trucks:
        _text(d, 0, h - 52, "No truck had a delivery in this period.", 9, MUTED)
        return d
    left = 30 * mm
    plot_w = w - left - 12 * mm
    vmax = max(sum(t[k] for k, _, _ in parts) for t in trucks) or 1
    y = h - 62
    for t in trucks:
        _text(d, left - 4, y + 3, t["plate"], 8.5, INK, FONT, "end")
        x = left
        total = 0
        for k, _, color in parts:
            v = t.get(k, 0)
            if not v:
                continue
            bw = plot_w * v / vmax
            d.add(Rect(x, y, max(bw - 2, 1), 5.5 * mm * 0.62, rx=1.5, ry=1.5, fillColor=color, strokeColor=None))
            x += bw
            total += v
        _text(d, x + 3, y + 3, str(total), 8, INK_2)
        y -= row_h
    return d


class OnTime(Flowable):
    """On time vs late, as a number with a thin bar under it (a two-part split
    is clearer as a number than as a pie)."""

    def __init__(self, report: dict):
        super().__init__()
        self.ot = report["on_time"]
        self.width, self.height = CONTENT_W, 30 * mm

    def wrap(self, aw, ah):
        return self.width, self.height

    def draw(self):
        c = self.canv
        on, late, nodue = self.ot["on_time"], self.ot["late"], self.ot["no_due"]
        n = on + late
        c.setFillColor(INK)
        c.setFont(BOLD, 11)
        c.drawString(0, self.height - 11, "On time")
        c.setFillColor(MUTED)
        c.setFont(FONT, 8)
        c.drawString(0, self.height - 24, "Completed deliveries that had a promised time")
        c.setFillColor(INK)
        c.setFont(BOLD, 22)
        c.drawString(0, 9 * mm, _pct(on, n))
        c.setFillColor(INK_2)
        c.setFont(FONT, 9)
        c.drawString(28 * mm, 11 * mm, f"{on} on time · {late} late" + (f" · {nodue} had no due time" if nodue else ""))
        bar_w, y = self.width - 28 * mm, 5 * mm
        c.setFillColor(GRID)
        c.roundRect(28 * mm, y, bar_w, 4, 2, stroke=0, fill=1)
        if n:
            ow = bar_w * on / n
            c.setFillColor(GOOD)
            c.roundRect(28 * mm, y, max(ow - 1, 0.1), 4, 2, stroke=0, fill=1)
            if late:
                c.setFillColor(SERIOUS)
                c.roundRect(28 * mm + ow + 1, y, max(bar_w - ow - 1, 0.1), 4, 2, stroke=0, fill=1)


def delivery_table(report: dict, tz) -> LongTable:
    """Every delivery in the period: reference, customer, status, truck,
    created, finished."""
    body = ParagraphStyle("cell", fontName=FONT, fontSize=7.5, leading=9, textColor=INK)

    def when(iso):
        if not iso:
            return "–"
        t = datetime.fromisoformat(iso.replace("Z", "+00:00")).astimezone(tz)
        return t.strftime("%d %b %H:%M")

    rows = [["Reference", "Customer", "Status", "Truck", "Created", "Finished"]]
    for r in report["deliveries"]:
        rows.append([r["reference"], Paragraph(str(r["customer_name"])[:60], body),
                     r["status"].replace("_", " "), r.get("plate") or "–",
                     when(r["created_at"]), when(r.get("finished_at"))])
    widths = [32 * mm, 52 * mm, 22 * mm, 24 * mm, 24 * mm, CONTENT_W - 154 * mm]
    t = LongTable(rows, colWidths=widths, repeatRows=1)
    status_col = {"completed": GOOD, "failed": CRITICAL, "cancelled": MUTED}
    style = [
        ("FONT", (0, 0), (-1, 0), BOLD, 7.5),
        ("FONT", (0, 1), (-1, -1), FONT, 7.5),
        ("TEXTCOLOR", (0, 0), (-1, 0), INK_2),
        ("TEXTCOLOR", (0, 1), (-1, -1), INK),
        ("LINEBELOW", (0, 0), (-1, 0), 0.8, INK),
        ("LINEBELOW", (0, 1), (-1, -1), 0.3, GRID),
        ("VALIGN", (0, 0), (-1, -1), "MIDDLE"),
        ("TOPPADDING", (0, 0), (-1, -1), 3),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 3),
        ("LEFTPADDING", (0, 0), (-1, -1), 3),
    ]
    for i, r in enumerate(report["deliveries"], 1):
        col = status_col.get(r["status"])
        if col is not None:
            style.append(("TEXTCOLOR", (2, i), (2, i), col))
            style.append(("FONT", (2, i), (2, i), BOLD, 7.5))
    t.setStyle(TableStyle(style))
    return t


def build_pdf(report: dict, meta: ReportMeta, charts: list[str], include_table: bool, tz) -> tuple[bytes, int]:
    """The PDF and its page count."""
    charts = list(CHARTS) if not charts or "all" in charts else [c for c in CHARTS if c in charts]
    buf = io.BytesIO()
    pages = {"n": 0}

    def frame(c, doc):
        pages["n"] = doc.page
        c.saveState()
        c.setFillColor(SURFACE)
        c.rect(0, 0, PAGE_W, PAGE_H, stroke=0, fill=1)
        c.setFillColor(MUTED)
        c.setFont(FONT, 7)
        c.drawString(MARGIN, 9 * mm, f"{meta.company} · {meta.title} · {meta.period_label}")
        c.drawRightString(PAGE_W - MARGIN, 9 * mm, f"Page {doc.page}")
        c.restoreState()

    doc = SimpleDocTemplate(buf, pagesize=A4, leftMargin=MARGIN, rightMargin=MARGIN,
                            topMargin=MARGIN, bottomMargin=16 * mm, title=meta.title, author=meta.company,
                            subject=meta.period_label, creator="Dispatch Copilot")
    h1 = ParagraphStyle("h1", fontName=BOLD, fontSize=19, leading=23, textColor=INK)
    sub = ParagraphStyle("sub", fontName=FONT, fontSize=9, leading=12, textColor=MUTED)
    note = ParagraphStyle("note", fontName=FONT, fontSize=8, leading=11, textColor=MUTED)

    story = [
        Paragraph(meta.company.upper(), ParagraphStyle("k", fontName=BOLD, fontSize=7.5, textColor=SERIES_1, leading=10)),
        Paragraph(meta.title, h1),
        Paragraph(f"{meta.period_label} · times in {meta.timezone} · made {meta.made_at.strftime('%d %b %Y, %H:%M')}", sub),
        Spacer(1, 6 * mm),
        Tiles(report),
        Spacer(1, 8 * mm),
    ]
    parts = {"status": lambda: status_chart(report), "daily": lambda: daily_chart(report),
             "by_truck": lambda: truck_chart(report), "on_time": lambda: OnTime(report)}
    for name in charts:
        story.append(KeepTogether([parts[name](), Spacer(1, 8 * mm)]))
    if report.get("total", 0) == 0:
        story.append(Paragraph("No deliveries were created in this period.", sub))
    if include_table and report["deliveries"]:
        story += [Spacer(1, 2 * mm), Paragraph("Deliveries", ParagraphStyle("h2", fontName=BOLD, fontSize=11, leading=14, textColor=INK)),
                  Spacer(1, 2 * mm), delivery_table(report, tz)]
        if report.get("truncated"):
            story.append(Paragraph("Only the first 2,000 deliveries are listed.", note))
    doc.build(story, onFirstPage=frame, onLaterPages=frame)
    return buf.getvalue(), pages["n"]
