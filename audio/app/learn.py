import base64
import hashlib
import io
import json
import math
import os
import select
import shutil
import subprocess
import threading
import time
import wave
from urllib.parse import parse_qs, urlsplit

import numpy as np

from .decision import classify, summarize_scores
from .stream import _chunks
from .yamnet import WINDOW_SAMPLES

SAMPLE_RATE = 8000
SEGMENT_SECONDS = 5
SEGMENT_BYTES = SAMPLE_RATE * SEGMENT_SECONDS * 2
FRAME_SAMPLES = 800
FRAME_COUNT = SAMPLE_RATE * SEGMENT_SECONDS // FRAME_SAMPLES
BAND_COUNT = 24
SIMILARITY_THRESHOLD = 0.80
MAX_CANDIDATES = 100


def learn(handler, classifier=None, classifier_lock=None):
    params = parse_qs(urlsplit(handler.path).query)
    mode = params.get("mode", ["fingerprint"])[0]
    try:
        duration = int(params.get("duration", ["14400"])[0])
    except ValueError:
        handler.send_error(400, "invalid learning duration")
        return
    expected_duration = {"fingerprint": 14400, "transitions": 3600}.get(mode)
    if duration != expected_duration:
        handler.send_error(400, "invalid learning mode or duration")
        return
    if mode == "transitions" and (classifier is None or classifier_lock is None):
        handler.send_error(503, "audio classifier unavailable")
        return
    sample_rate = 8000 if mode == "fingerprint" else 16000
    segment_seconds = 5 if mode == "fingerprint" else 1
    segment_bytes = sample_rate * segment_seconds * 2
    ffmpeg = os.getenv("FFMPEG_BIN") or shutil.which("ffmpeg")
    if not ffmpeg:
        handler.send_error(503, "FFmpeg unavailable")
        return
    command = [ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1",
               "-probesize", "8192", "-analyzeduration", "0", "-i", "pipe:0", "-map", "0:a:0", "-vn",
               "-f", "s16le", "-acodec", "pcm_s16le", "-ar", str(sample_rate), "-ac", "1", "pipe:1"]
    try:
        proc = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=segment_bytes)
    except OSError:
        handler.send_error(503, "could not start FFmpeg")
        return

    started = time.monotonic()
    handler.close_connection = True
    feeder = threading.Thread(target=_feed, args=(handler, proc), daemon=True)
    feeder.start()
    handler.send_response(200)
    handler.send_header("Content-Type", "application/x-ndjson")
    handler.send_header("Transfer-Encoding", "chunked")
    handler.send_header("Cache-Control", "no-store")
    handler.end_headers()
    history = []
    candidates = []
    captures = []
    stable_category = pending_category = None
    pending_count = 0
    pending_before = pending_after = []
    pending_elapsed = pending_confidence = 0
    transition_index = 0
    segment_index = 0
    candidate_count = 0
    last_progress = 0
    try:
        _event(handler, {"type": "started", "duration_seconds": duration})
        while time.monotonic() - started < duration:
            ready, _, _ = select.select([proc.stdout], [], [], 10)
            if not ready:
                continue
            pcm = proc.stdout.read(segment_bytes)
            if len(pcm) != segment_bytes:
                if proc.poll() is not None:
                    raise RuntimeError("upstream stream ended during learning")
                continue
            segment_index += 1
            elapsed = int(time.monotonic() - started)
            samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32)
            if mode == "transitions":
                values = (samples[:WINDOW_SAMPLES] / 32768).astype(np.float32)
                with classifier_lock:
                    scores = classifier.predict(values).tolist()
                decision = classify(summarize_scores(classifier.labels, scores))
                if stable_category is None:
                    stable_category = decision.category
                elif decision.category == stable_category:
                    pending_category, pending_count = None, 0
                elif len(history) < SEGMENT_SECONDS and pending_category is None:
                    stable_category = decision.category
                elif decision.category != pending_category:
                    pending_category, pending_count = decision.category, 1
                    pending_before, pending_after = history[-SEGMENT_SECONDS:], [pcm]
                    pending_elapsed = max(0, segment_index - segment_seconds)
                    pending_confidence = decision.confidence
                else:
                    pending_count += 1
                    pending_after.append(pcm)
                    pending_confidence = decision.confidence
                if pending_category and pending_count >= 2 and len(pending_before) == SEGMENT_SECONDS:
                    captures.append({"before": list(pending_before), "after": list(pending_after),
                                     "last_index": segment_index, "previous": stable_category,
                                     "current": pending_category, "elapsed": pending_elapsed,
                                     "confidence": pending_confidence})
                    stable_category, pending_category, pending_count = pending_category, None, 0
                current_index = segment_index
                ready_captures = []
                for capture in captures:
                    if capture["last_index"] < current_index:
                        capture["after"].append(pcm)
                        capture["last_index"] = current_index
                    if len(capture["after"]) >= SEGMENT_SECONDS:
                        preview = io.BytesIO()
                        with wave.open(preview, "wb") as output:
                            output.setnchannels(1)
                            output.setsampwidth(2)
                            output.setframerate(sample_rate)
                            output.writeframes(b"".join(capture["before"] + capture["after"][:SEGMENT_SECONDS]))
                        transition_index += 1
                        candidate_count += 1
                        _event(handler, {"type": "transition", "transition_index": transition_index,
                                         "previous_category": capture["previous"], "current_category": capture["current"],
                                         "confidence": round(float(capture["confidence"]), 5),
                                         "changed_elapsed_seconds": capture["elapsed"],
                                         "preview_wav": base64.b64encode(preview.getvalue()).decode("ascii"),
                                         "candidate_count": candidate_count})
                        ready_captures.append(capture)
                for capture in ready_captures:
                    captures.remove(capture)
                history.append(pcm)
                if len(history) > SEGMENT_SECONDS:
                    history.pop(0)
            elif math.sqrt(float(np.mean(samples * samples))) >= 100:
                current = _features(samples)
                eligible = [(seen, vector, signature) for seen, vector, signature in history if elapsed - seen >= 60]
                if eligible:
                    matrix = np.asarray([item[1] for item in eligible], dtype=np.float32)
                    scores = matrix @ current
                    best_index = int(np.argmax(scores))
                    similarity = float(scores[best_index])
                    if similarity > SIMILARITY_THRESHOLD and candidate_count < MAX_CANDIDATES:
                        reference_time, reference, fingerprint = eligible[best_index]
                        if not any(float(candidate @ current) >= SIMILARITY_THRESHOLD for candidate in candidates):
                            candidates.append(current)
                            candidate_count += 1
                            preview = io.BytesIO()
                            with wave.open(preview, "wb") as output:
                                output.setnchannels(1)
                                output.setsampwidth(2)
                                output.setframerate(SAMPLE_RATE)
                                output.writeframes(pcm)
                            _event(handler, {"type": "candidate", "fingerprint": fingerprint,
                                             "preview_wav": base64.b64encode(preview.getvalue()).decode("ascii"),
                                             "first_elapsed_seconds": reference_time, "last_elapsed_seconds": elapsed,
                                             "occurrences": 2, "similarity": round(similarity, 5)})
                signature = base64.b64encode(np.rint((current.reshape(FRAME_COUNT, BAND_COUNT) + 1) * 127.5).clip(0, 255).astype(np.uint8).tobytes()).decode("ascii")
                history.append((elapsed, current, signature))
            if elapsed - last_progress >= 30:
                _event(handler, {"type": "progress", "elapsed_seconds": elapsed, "candidate_count": candidate_count})
                last_progress = elapsed
        _event(handler, {"type": "complete", "elapsed_seconds": duration, "candidate_count": candidate_count})
    except (BrokenPipeError, ConnectionError, OSError) as error:
        try:
            _event(handler, {"type": "error", "error": str(error)})
        except (BrokenPipeError, ConnectionError, OSError):
            pass
    except Exception as error:
        try:
            _event(handler, {"type": "error", "error": f"{type(error).__name__}: {error}"})
        except (BrokenPipeError, ConnectionError, OSError):
            pass
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=2)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        feeder.join(timeout=2)
        proc.stdout.close()
        try:
            handler.wfile.write(b"0\r\n\r\n")
            handler.wfile.flush()
        except (BrokenPipeError, ConnectionError, OSError):
            pass


def _features(samples):
    frames = samples.reshape(FRAME_COUNT, FRAME_SAMPLES)
    spectrum = np.abs(np.fft.rfft(frames * np.hanning(FRAME_SAMPLES), n=1024)) ** 2
    edges = np.geomspace(1, spectrum.shape[1], BAND_COUNT + 1).astype(int)
    bands = np.stack([np.log1p(spectrum[:, edges[i]:max(edges[i] + 1, edges[i + 1])].mean(axis=1)) for i in range(BAND_COUNT)], axis=1)
    bands -= bands.mean(axis=1, keepdims=True)
    norms = np.linalg.norm(bands, axis=1, keepdims=True)
    bands /= np.maximum(norms, 1e-8)
    features = bands.reshape(-1).astype(np.float32)
    return features / max(float(np.linalg.norm(features)), 1e-8)


def _feed(handler, proc):
    try:
        for block in _chunks(handler):
            proc.stdin.write(block)
            proc.stdin.flush()
    except (BrokenPipeError, ConnectionError, OSError, ValueError):
        pass
    finally:
        try:
            proc.stdin.close()
        except OSError:
            pass


def _event(handler, event):
    data = json.dumps(event, separators=(",", ":")).encode() + b"\n"
    handler.wfile.write(f"{len(data):x}\r\n".encode() + data + b"\r\n")
    handler.wfile.flush()
