#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["websockets>=13"]
# ///
"""Live check of lasso's text-to-speech relay against the real Inworld service.

Run it once the Inworld key is configured in lasso (Settings > Phone app speech,
or update_settings client_tts; docs/design/client-tts.md, "Turning it on"). It
makes a throwaway person and a paired device through the owner's /api, asks the
relay for the engine list, speaks one sentence over /client/v1/tts/stream, checks
the audio is plausible 16-bit PCM, writes it to a WAV file, and deletes the
throwaway person again. The device token and the Inworld key are never printed.

    scripts/tts-live-check.py [--url http://127.0.0.1:8090] [--model tts-2-realtime]
                              [--voice <id>] [--rate 24000] [--out tts-live-check.wav]
                              [--text "..."]

Set UI_AUTH=user:password in the environment if lasso runs with UI_AUTH (it is
sent to the owner's /api only). The sentence is counted against the month's
usage like any other (about 60 characters). Exit status 0 when every check passed.
"""
from __future__ import annotations

import argparse
import asyncio
import base64
import json
import math
import os
import struct
import sys
import time
import urllib.error
import urllib.request
import wave

import websockets

failed = False


def check(ok: bool, what: str, detail: str = "") -> None:
    global failed
    print(("ok    " if ok else "FAIL  ") + what + (f"  ({detail})" if detail else ""))
    if not ok:
        failed = True


def http(method: str, url: str, body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode("utf-8", "replace")[:200]


def owner_headers() -> dict:
    ua = os.environ.get("UI_AUTH", "")
    if not ua:
        return {}
    return {"Authorization": "Basic " + base64.b64encode(ua.encode()).decode()}


async def speak(base: str, token: str, model: str, voice: str, rate: int, text: str):
    ws_url = base.replace("http", "ws", 1) + "/client/v1/tts/stream"
    t0 = time.monotonic()
    pcm = bytearray()
    frames: list[dict] = []
    first_audio = None
    bad_pairs: list[int] = []
    async with websockets.connect(ws_url, additional_headers={"Authorization": f"Bearer {token}"}, max_size=8 << 20) as ws:
        start = {"type": "start", "engine": "inworld", "model": model, "sample_rate": rate}
        if voice:
            start["voice"] = voice
        await ws.send(json.dumps(start))
        ready = json.loads(await ws.recv())
        frames.append(ready)
        ready_ms = (time.monotonic() - t0) * 1000
        if ready.get("type") != "ready":
            return ready, pcm, frames, ready_ms, None, None
        t1 = time.monotonic()
        await ws.send(json.dumps({"type": "text", "id": "live1", "text": text}))
        await ws.send(json.dumps({"type": "end"}))
        pending = None
        done = None
        while True:
            try:
                msg = await asyncio.wait_for(ws.recv(), timeout=60)
            except websockets.ConnectionClosed:
                break
            if isinstance(msg, bytes):
                if not (pending is not None and pending["bytes"] == len(msg)):
                    bad_pairs.append(len(msg))
                pcm.extend(msg)
                pending = None
                continue
            m = json.loads(msg)
            frames.append(m)
            if m["type"] == "audio":
                if first_audio is None:
                    first_audio = (time.monotonic() - t1) * 1000
                pending = m
            elif m["type"] == "done":
                done = m
            elif m["type"] == "error":
                break
        check(not bad_pairs, "every audio header is followed by one binary frame of that size", f"{len(bad_pairs)} mismatches")
        return ready, pcm, frames, ready_ms, first_audio, done


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default=os.environ.get("LASSO_URL", "http://127.0.0.1:8090"))
    ap.add_argument("--model", default="tts-2-realtime")
    ap.add_argument("--voice", default="")
    ap.add_argument("--rate", type=int, default=24000)
    ap.add_argument("--out", default="tts-live-check.wav")
    ap.add_argument("--text", default="Hello from lasso. This is a check of the speech relay.")
    a = ap.parse_args()
    base = a.url.rstrip("/")
    oh = owner_headers()

    st, cfg = http("GET", base + "/api/client-tts", headers=oh)
    check(st == 200 and isinstance(cfg, dict), "owner API answers /api/client-tts", f"status {st}")
    if st != 200:
        return 1
    print(f"      engine state: {cfg['inworld']['state']}, key reference set: {bool(cfg['inworld']['key_ref'])}, enabled: {cfg['inworld']['enabled']}")
    check(cfg["inworld"]["state"] in ("ready", "failing"), "the key reference reads (state ready)")

    st, user = http("POST", base + "/api/app-users", {"name": "tts live check"}, oh)
    if st != 200:
        check(False, "created a throwaway person", f"status {st}")
        return 1
    uid = user["id"] if "id" in user else user["user"]["id"]
    try:
        st, pr = http("POST", f"{base}/api/app-users/{uid}/pairings", {}, oh)
        check(st == 200 and "code" in pr, "minted a pairing code", f"status {st}")
        st, paired = http("POST", base + "/client/v1/pair", {"code": pr["code"], "label": "tts live check", "platform": "other"})
        check(st == 200 and "token" in paired, "paired a throwaway device", f"status {st}")
        token = paired["token"]
        auth = {"Authorization": f"Bearer {token}"}

        st, me = http("GET", base + "/client/v1/me", headers=auth)
        check(st == 200 and "tts" in me.get("features", []), "/me features lists tts")

        st, tts = http("GET", base + "/client/v1/tts", headers=auth)
        check(st == 200, "GET /client/v1/tts", f"status {st}")
        eng = tts["engines"][0]
        print(f"      available={eng['available']} reason={eng['reason']} voices={len(eng['voices'])} default_voice={eng['default_voice']!r} usage={eng['usage']}")
        check(eng["available"], "the engine is available")
        check(len(eng["voices"]) > 0, "Inworld's voice catalog came back")
        if eng["voices"]:
            v = eng["voices"][0]
            check({"id", "name", "gender", "language", "description"} <= set(v), "a voice has id, name, gender, language, description", str(sorted(v)))
        voice = a.voice or eng["default_voice"] or (eng["voices"][0]["id"] if eng["voices"] else "")
        if not eng["available"] or not voice:
            return 1

        ready, pcm, frames, ready_ms, first_audio, done = asyncio.run(speak(base, token, a.model, voice, a.rate, a.text))
        check(ready.get("type") == "ready", "start answered ready", json.dumps({k: v for k, v in ready.items() if k != "message"}))
        errs = [f for f in frames if f.get("type") == "error"]
        check(not errs, "no error frames", json.dumps(errs[:1]))
        check(done is not None, "a done frame arrived")
        if ready.get("type") == "ready":
            print(f"      ready after {ready_ms:.0f} ms (includes connecting to Inworld)")
        if first_audio is not None:
            print(f"      first audio {first_audio:.0f} ms after the text was sent")
        check(len(pcm) > 0 and len(pcm) % 2 == 0, "audio is a whole number of 16-bit samples", f"{len(pcm)} bytes")
        if len(pcm) >= 2:
            n = len(pcm) // 2
            samples = struct.unpack(f"<{n}h", bytes(pcm[: n * 2]))
            peak = max(abs(s) for s in samples)
            rms = math.sqrt(sum(s * s for s in samples) / n)
            secs = n / a.rate
            print(f"      {secs:.2f} s of audio at {a.rate} Hz, peak {peak}, rms {rms:.0f}")
            check(0.5 < secs < 30, "a plausible duration for one sentence")
            check(100 < rms < 20000 and peak < 32768, "plausible speech level (little-endian s16)", f"peak {peak} rms {rms:.0f}")
            # A byte-swapped stream looks like loud noise: its rms is far above speech.
            swapped = struct.unpack(f">{n}h", bytes(pcm[: n * 2]))
            rms_sw = math.sqrt(sum(s * s for s in swapped) / n)
            check(rms <= rms_sw * 1.5 or rms < 6000, "not byte-swapped", f"le rms {rms:.0f}, be rms {rms_sw:.0f}")
            if done:
                check(abs(done["audio_ms"] - secs * 1000) < 50, "done.audio_ms matches the bytes received", f"{done['audio_ms']} vs {secs * 1000:.0f}")
                check(done["characters"] == len(a.text), "done.characters counts the text", f"{done['characters']} vs {len(a.text)}")
            with wave.open(a.out, "wb") as w:
                w.setnchannels(1)
                w.setsampwidth(2)
                w.setframerate(a.rate)
                w.writeframes(bytes(pcm[: n * 2]))
            print(f"      wrote {a.out} (play it and listen)")

        st, tts2 = http("GET", base + "/client/v1/tts", headers=auth)
        used = tts2["engines"][0]["usage"]["characters_this_month"] if st == 200 else None
        check(used is not None and used >= len(a.text), "usage this month counts the sentence", f"{used}")
    finally:
        st, _ = http("DELETE", f"{base}/api/app-users/{uid}", headers=oh)
        print(f"      removed the throwaway person (status {st})")
    print("\nAll checks passed." if not failed else "\nSome checks FAILED.")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
