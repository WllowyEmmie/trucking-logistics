"""The agent's prompts, one Markdown file each, versioned by content.

A prompt's version is its name and a hash of its text (ask@1a2b3c4d), so it
changes whenever the wording does, with nothing to remember to bump. Every
run logs the version it used, and traces carry it, so a change in behaviour
can be tied to the prompt change that caused it."""

import hashlib
from dataclasses import dataclass
from pathlib import Path

HERE = Path(__file__).parent


@dataclass(frozen=True)
class Prompt:
    name: str
    text: str
    version: str


def load(name: str, folder: Path = HERE) -> Prompt:
    # Line endings normalised: the same text hashes the same on Windows.
    text = (folder / f"{name}.md").read_text(encoding="utf-8").replace("\r\n", "\n").strip()
    digest = hashlib.sha256(text.encode()).hexdigest()[:8]
    return Prompt(name, text, f"{name}@{digest}")


ASK = load("ask")  # the console's ask box
PROPOSE = load("propose")  # the timed proposal pass
SUPERVISE = load("supervise")  # auto mode's contested-truck call
