"""Talk to the Copilot from a terminal, before the console has its chat pane.

    cd agent
    python -m scripts.copilot_cli --email you@example.com
    python -m scripts.copilot_cli --email you@example.com --demo   # adds two test deliveries first

Signs in to the dispatch API as you (the password is asked for, never
stored), then each line you type goes to POST /copilot/chat. Answers stream
in; when a change needs approval you're asked approve / reject for each one
(or edit, where allowed), and the answer goes to POST /copilot/resume.
Type "new" for a new conversation, "quit" to stop. Answers are rendered
Markdown (bold, lists, tables) when `rich` is installed: pip install -e ".[dev]".

--demo creates two pending deliveries for "Mr Adebayo" and "Mr Owere" so
there's something to cancel. Use it only against a local or test API."""

import argparse
import getpass
import json
import sys

import httpx

COLOR = sys.stdout.isatty()


def dim(s: str) -> str:
    return f"\033[2m{s}\033[0m" if COLOR else s


def bold(s: str) -> str:
    return f"\033[1m{s}\033[0m" if COLOR else s


def sign_in(api: str, email: str) -> str:
    password = getpass.getpass(f"Password for {email}: ")
    res = httpx.post(f"{api}/auth/login", json={"email": email, "password": password}, timeout=15)
    if res.status_code != 200:
        sys.exit(f"Sign-in failed: {res.status_code} {res.text}")
    # From the Set-Cookie header itself: the cookie is marked Secure, and a
    # cookie jar won't keep it for a plain http://localhost address.
    token = None
    for header in res.headers.get_list("set-cookie"):
        name, _, rest = header.partition("=")
        if name.strip() == "session":
            token = rest.split(";", 1)[0]
    if not token:
        sys.exit("Signed in, but no session cookie came back.")
    return token


def make_demo(api: str, token: str) -> None:
    h = {"Authorization": f"Bearer {token}"}
    for name, pickup, drop in (
        ("Mr Adebayo", ("Apapa Wharf", 6.4433, 3.3647), ("Ikeja GRA", 6.5800, 3.3500)),
        ("Mr Owere", ("Yaba", 6.5100, 3.3800), ("Lekki Phase 1", 6.4470, 3.4720)),
    ):
        body = {"customer_name": name, "stops": [
            {"stop_type": "pickup", "address": pickup[0], "lat": pickup[1], "lng": pickup[2]},
            {"stop_type": "dropoff", "address": drop[0], "lat": drop[1], "lng": drop[2]},
        ]}
        res = httpx.post(f"{api}/deliveries", json=body, headers=h, timeout=15)
        if res.status_code >= 300:
            sys.exit(f"Could not create the demo delivery for {name}: {res.status_code} {res.text}")
        print(dim(f"created {res.json()['delivery']['reference']} for {name}"))


class Reply:
    """Shows the Copilot's answer as rendered Markdown while it streams:
    bold, lists, tables, code, links, in any terminal (bash, PowerShell,
    cmd), using the `rich` library. Without rich, the text is printed as
    it comes. Other lines (tool steps) go above the answer."""

    def __init__(self) -> None:
        try:
            from rich.console import Console
            from rich.live import Live
            from rich.markdown import Markdown
        except ImportError:
            self.rich = None
        else:
            self.rich = (Console(), Live, Markdown)
        self.buf = ""
        self.live = None

    def token(self, text: str) -> None:
        self.buf += text
        if not self.rich:
            print(text, end="", flush=True)
            return
        console, Live, Markdown = self.rich
        if self.live is None:
            self.live = Live(Markdown(self.buf), console=console, refresh_per_second=12, vertical_overflow="visible")
            self.live.start()
        else:
            self.live.update(Markdown(self.buf))

    def done(self, text: str) -> None:
        """The whole reply: replaces what streamed (they're the same text)."""
        if not self.rich:
            if not self.buf:
                print(text, end="")
            print()
        else:
            console, _, Markdown = self.rich
            if self.live is not None:
                self.live.update(Markdown(text))
                self.live.stop()
            else:
                console.print(Markdown(text))
            console.print()
        self.buf, self.live = "", None

    def note(self, line: str) -> None:
        if self.rich and self.live is not None:
            from rich.text import Text

            # from_ansi: keep the dim colour, and don't read "[read]" as markup.
            self.live.console.print(Text.from_ansi(line))
        else:
            print(line)


def save_file(client: httpx.Client, info: dict) -> str:
    """Downloads a file the Copilot made (a PDF report) into this folder."""
    res = client.get(info["url"], timeout=60)
    if res.status_code != 200:
        return f"couldn't download {info['name']}: {res.status_code}"
    name = "".join(c if c.isalnum() or c in "-_." else "_" for c in info["name"])
    with open(name, "wb") as f:
        f.write(res.content)
    import os

    return f"saved {os.path.abspath(name)} ({len(res.content) // 1024} KB)"


def stream(client: httpx.Client, path: str, body: dict) -> tuple[str | None, dict | None]:
    """Prints a turn as it streams. Returns (thread_id, pending approval)."""
    thread, pending, event = None, None, None
    reply = Reply()
    with client.stream("POST", path, json=body, timeout=180) as res:
        if res.status_code != 200:
            res.read()
            print(f"! {res.status_code}: {res.text}")
            return None, None
        for line in res.iter_lines():
            if line.startswith("event: "):
                event = line[7:]
                continue
            if not line.startswith("data: "):
                continue
            data = json.loads(line[6:])
            if event == "thread":
                thread = data["thread_id"]
            elif event == "token":
                reply.token(data["text"])
            elif event == "message":
                reply.done(data["text"])
            elif event == "tool_start":
                reply.note(dim(f"  -> {data['name']}({json.dumps(data['args'])})  [{data['tier']}]"))
            elif event == "tool_end":
                mark = "ok" if data["ok"] else "refused"
                reply.note(dim(f"  <- {data['name']}: {mark}"))
            elif event == "ui":
                reply.note(dim(f"  [console] {data}"))
            elif event == "file":
                reply.note(dim(f"  [file] {save_file(client, data)}"))
            elif event == "interrupt":
                pending = data
            elif event == "error":
                reply.note(f"! {data['message']}")
    return thread, pending


def decide(pending: dict) -> list[dict]:
    print(bold("\nWaiting for your approval:"))
    decisions = []
    for i, a in enumerate(pending["actions"], 1):
        print(f"  {i}. {bold(a['name'])} [{a['tier']}] {json.dumps(a['args'])}")
        options = "/".join(a["allowed_decisions"])
        while True:
            choice = input(f"     {options}? ").strip().lower()
            if choice in ("a", "approve") and "approve" in a["allowed_decisions"]:
                decisions.append({"type": "approve"})
                break
            if choice in ("r", "reject") and "reject" in a["allowed_decisions"]:
                note = input("     note for the Copilot (optional): ").strip()
                decisions.append({"type": "reject", "message": note or "Rejected."})
                break
            if choice in ("e", "edit") and "edit" in a["allowed_decisions"]:
                raw = input(f"     new args as JSON [{json.dumps(a['args'])}]: ").strip()
                try:
                    decisions.append({"type": "edit", "args": json.loads(raw) if raw else a["args"]})
                    break
                except ValueError:
                    print("     not valid JSON")
                    continue
            print(f"     type one of: {options}")
    return decisions


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--email", required=True)
    ap.add_argument("--api", default="http://localhost:8080", help="the dispatch API")
    ap.add_argument("--agent", default="http://localhost:8200", help="the agent")
    ap.add_argument("--demo", action="store_true", help="create two test deliveries first")
    args = ap.parse_args()

    token = sign_in(args.api.rstrip("/"), args.email)
    if args.demo:
        make_demo(args.api.rstrip("/"), token)
    client = httpx.Client(base_url=args.agent.rstrip("/"), headers={"Authorization": f"Bearer {token}"})
    thread = None
    print(dim("Signed in. Talk to the Copilot; 'new' starts over, 'quit' stops.\n"))
    while True:
        try:
            text = input(bold("you> ")).strip()
        except (EOFError, KeyboardInterrupt):
            break
        if not text:
            continue
        if text == "quit":
            break
        if text == "new":
            thread = None
            print(dim("(new conversation)"))
            continue
        body = {"message": text, **({"thread_id": thread} if thread else {})}
        thread_now, pending = stream(client, "/copilot/chat", body)
        thread = thread_now or thread
        while pending and thread:
            decisions = decide(pending)
            _, pending = stream(client, "/copilot/resume", {"thread_id": thread, "decisions": decisions})


if __name__ == "__main__":
    main()
