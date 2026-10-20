def duration(minutes: float) -> str:
    """"26 min", "2 h 14 min", "3 days 4 h", from a week "9 days": as the console writes it."""
    m = max(0, int(round(minutes)))
    if m < 60:
        return f"{m} min"
    if m < 24 * 60:
        return f"{m // 60} h {m % 60} min" if m % 60 else f"{m // 60} h"
    d, h = m // 1440, (m % 1440) // 60
    days = f"{d} day" if d == 1 else f"{d} days"
    return days if d >= 7 or not h else f"{days} {h} h"
