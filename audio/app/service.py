import json
import os
import resource
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

import numpy as np

from .audio import read_audio_bytes
from .decision import classify, summarize_scores
from .learn import learn as learn_stream
from .non_music import configure as configure_non_music
from .stream import process as process_stream
from .yamnet import SAMPLE_RATE, WINDOW_SAMPLES, Yamnet, windows


ROOT = Path(__file__).resolve().parents[1]
MODEL = Path(os.getenv("YAMNET_MODEL", ROOT / "models/yamnet.tflite"))
LABELS = Path(os.getenv("YAMNET_LABELS", ROOT / "models/yamnet_class_map.csv"))
classifier = Yamnet(MODEL, LABELS)
classifier_lock = threading.Lock()
suppression_states = {}
marker_suppression = {}
marker_versions = {}
suppression_lock = threading.Lock()


def set_suppression(station, active):
    if not station:
        return
    with suppression_lock:
        if active:
            suppression_states[station] = True
        else:
            suppression_states.pop(station, None)


def is_marker_suppressed(station):
    with suppression_lock:
        return marker_suppression.get(station, False)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        request = urlsplit(self.path)
        if request.path == "/suppression":
            station = parse_qs(request.query).get("station", [""])[0]
            with suppression_lock:
                active = suppression_states.get(station, False)
            self._json(200, {"suppressed": active})
            return
        if request.path == "/healthz":
            self._json(200, {"status": "ok", "analysis_only": True})
            return
        self.send_error(404)

    def do_POST(self):
        if self.path == "/suppression/marker":
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 1 or length > 4096:
                    raise ValueError("invalid body size")
                payload = json.loads(self.rfile.read(length))
                station = payload["station"]
                active = payload["suppressed"]
                version = int(payload["version"])
                if not isinstance(station, str) or not station or type(active) is not bool or version < 1:
                    raise ValueError("invalid marker state")
            except (KeyError, TypeError, ValueError, json.JSONDecodeError):
                self.send_error(400, "invalid suppression marker")
                return
            with suppression_lock:
                if version >= marker_versions.get(station, 0):
                    marker_versions[station] = version
                    marker_suppression[station] = active
            self._json(200, {"ok": True})
            return
        if self.path.split("?", 1)[0] == "/process":
            buffered_audio = None
            if "mode=non-music" in self.path.split("?", 1)[-1].split("&"):
                try:
                    buffered_audio = configure_non_music(self, classifier, classifier_lock,
                                                         set_suppression, is_marker_suppressed)
                except ValueError:
                    self.send_error(400, "invalid non-music settings")
                    return
            process_stream(self, buffered_audio)
            return
        if self.path.split("?", 1)[0] == "/learn":
            learn_stream(self, classifier, classifier_lock)
            return
        if self.path == "/classify":
            self._classify_live_frame()
            return
        if self.path != "/analyze":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 1 or length > 8 * 1024 * 1024:
                self._json(413, {"error": "audio upload must be between 1 byte and 8 MiB"})
                return
            audio = read_audio_bytes(self.rfile.read(length))
            started = time.perf_counter()
            cpu_started = time.process_time()
            frames = []
            for frame in windows(audio):
                with classifier_lock:
                    values = classifier.predict(frame).tolist()
                scores = summarize_scores(classifier.labels, values)
                decision = classify(scores)
                frames.append({
                    "category": decision.category,
                    "confidence": round(decision.confidence, 5),
                    "speech": round(scores.speech, 5),
                    "singing": round(scores.singing, 5),
                    "music": round(scores.music, 5),
                    "jingle": round(scores.jingle, 5),
                    "target_gain": 1.0,
                })
            self._json(200, {
                "analysis_only": True,
                "automatic_filter_enabled": False,
                "frame_count": len(frames),
                "inference_wall_seconds": round(time.perf_counter() - started, 4),
                "inference_cpu_seconds": round(time.process_time() - cpu_started, 4),
                "peak_rss_kib": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss,
                "frames": frames,
            })
        except (ValueError, RuntimeError, subprocess.CalledProcessError) as error:
            self._json(400, {"error": str(error)})

    def _classify_live_frame(self):
        if int(self.headers.get("Content-Length", "0")) != WINDOW_SAMPLES * 4:
            self._json(400, {"error": "live audio frame must contain 0.975 seconds of 16 kHz float PCM"})
            return
        samples = np.frombuffer(self.rfile.read(WINDOW_SAMPLES * 4), dtype="<f4").copy()
        if samples.size != WINDOW_SAMPLES or not np.isfinite(samples).all():
            self._json(400, {"error": "invalid live audio frame"})
            return
        with classifier_lock:
            values = classifier.predict(samples)
        scores = summarize_scores(classifier.labels, values.tolist())
        decision = classify(scores)
        top = values.argsort()[-5:][::-1]
        self._json(200, {
            "category": decision.category,
            "confidence": round(decision.confidence, 5),
            "music": round(scores.music, 5),
            "jingle": round(scores.jingle, 5),
            "speech": round(scores.speech, 5),
            "singing": round(scores.singing, 5),
            "top_labels": [{"name": classifier.labels[index], "score": round(float(values[index]), 5)} for index in top],
            "sample_rate": SAMPLE_RATE,
            "analysis_only": True,
        })

    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        return


if __name__ == "__main__":
    server = ThreadingHTTPServer((os.getenv("LISTEN_ADDR", "0.0.0.0"), int(os.getenv("PORT", "8790"))), Handler)
    server.daemon_threads = True
    server.serve_forever()
