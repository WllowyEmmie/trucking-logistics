"""Files the Copilot makes (PDF reports), kept in memory for an hour.

Each file belongs to the dispatcher who asked for it: only they can
download it (GET /copilot/files/{id}). Ids are random and unguessable.
In memory, so a restart drops them: they're made in seconds and the chat
can make them again. Bounded per dispatcher and in total."""

import secrets
import threading
import time
from dataclasses import dataclass, field

TTL_S = 3600
MAX_PER_OWNER = 20
MAX_TOTAL_BYTES = 100 * 2**20
MAX_FILE_BYTES = 20 * 2**20


@dataclass
class StoredFile:
    id: str
    owner: str
    name: str
    mime: str
    data: bytes
    made: float = field(default_factory=time.time)


class FileStore:
    def __init__(self) -> None:
        self._files: dict[str, StoredFile] = {}
        self._lock = threading.Lock()

    def _sweep(self, now: float) -> None:
        for fid in [f.id for f in self._files.values() if now - f.made > TTL_S]:
            del self._files[fid]

    def put(self, owner: str, name: str, mime: str, data: bytes) -> StoredFile:
        if len(data) > MAX_FILE_BYTES:
            raise ValueError("file too large")
        now = time.time()
        with self._lock:
            self._sweep(now)
            mine = sorted((f for f in self._files.values() if f.owner == owner), key=lambda f: f.made)
            for old in mine[: max(0, len(mine) - MAX_PER_OWNER + 1)]:
                del self._files[old.id]
            while self._files and sum(len(f.data) for f in self._files.values()) + len(data) > MAX_TOTAL_BYTES:
                oldest = min(self._files.values(), key=lambda f: f.made)
                del self._files[oldest.id]
            f = StoredFile(secrets.token_urlsafe(18), owner, name, mime, data, now)
            self._files[f.id] = f
            return f

    def get(self, file_id: str, owner: str) -> StoredFile | None:
        with self._lock:
            self._sweep(time.time())
            f = self._files.get(file_id)
            return f if f is not None and secrets.compare_digest(f.owner, owner) else None


store = FileStore()
