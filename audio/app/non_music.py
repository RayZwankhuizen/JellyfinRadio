import base64
import binascii
from functools import partial
from urllib.parse import parse_qs, urlsplit

import numpy as np

from .decision import classify, summarize_scores
from .learn import BAND_COUNT, FRAME_COUNT, SEGMENT_SECONDS, _features
from .yamnet import WINDOW_SAMPLES


PCM_RATE = 48000
FRAMES_PER_SECOND = FRAME_COUNT // SEGMENT_SECONDS


def configure(handler, classifier, classifier_lock, set_suppression, is_marker_suppressed):
    params = parse_qs(urlsplit(handler.path).query)
    threshold = float(params.get("threshold", ["0.8"])[0])
    fade_down = float(params.get("fade_down", ["1.5"])[0])
    fade_up = float(params.get("fade_up", ["1.5"])[0])
    if not (0 <= threshold <= 1 and 0 <= fade_down <= 10 and 0 <= fade_up <= 10):
        raise ValueError("invalid non-music settings")

    references = []
    for signature in (handler.headers.get_all("X-Smart-Radio-Fingerprint") or [])[:64]:
        try:
            decoded = base64.b64decode(signature, validate=True)
        except (ValueError, binascii.Error):
            continue
        if len(decoded) == FRAME_COUNT * BAND_COUNT:
            vector = np.frombuffer(decoded, dtype=np.uint8).astype(np.float32) / 127.5 - 1
            references.append(vector.reshape(FRAME_COUNT, BAND_COUNT))
    return partial(buffered_audio, classifier=classifier, classifier_lock=classifier_lock,
                   references=np.asarray(references), threshold=threshold,
                   fade_down=fade_down, fade_up=fade_up,
                   station=handler.headers.get("X-Smart-Radio-Station", ""),
                   set_suppression=set_suppression, is_marker_suppressed=is_marker_suppressed)


def matched_frames(mono, references):
    matches = np.zeros(FRAME_COUNT, dtype=bool)
    if not len(references) or np.sqrt(np.mean(mono * mono)) < 100:
        return matches
    frames = _features(mono).reshape(FRAME_COUNT, BAND_COUNT)
    # The playback connection and the learning session start on different boundaries.
    for shift in range(-FRAME_COUNT // 2, FRAME_COUNT // 2 + 1):
        start, end = max(shift, 0), min(FRAME_COUNT + shift, FRAME_COUNT)
        current = frames[start:end].reshape(-1)
        candidates = references[:, start - shift:end - shift].reshape(len(references), -1)
        norms = np.linalg.norm(candidates, axis=1) * np.linalg.norm(current)
        scores = np.sum(candidates * current, axis=1) / np.maximum(norms, 1e-8)
        if np.any(scores >= 0.88):
            matches[start:end] = True
    return matches


def buffered_audio(source, gain, *, classifier, classifier_lock, references, threshold, fade_down, fade_up,
                   station="", set_suppression=None, is_marker_suppressed=None):
    if set_suppression:
        set_suppression(station, False)
    try:
        yield from _process_audio(source, gain, classifier=classifier, classifier_lock=classifier_lock,
                                  references=references, threshold=threshold, fade_down=fade_down,
                                  fade_up=fade_up, station=station, set_suppression=set_suppression,
                                  is_marker_suppressed=is_marker_suppressed)
    finally:
        if set_suppression:
            set_suppression(station, False)


def _process_audio(source, gain, *, classifier, classifier_lock, references, threshold, fade_down, fade_up,
                   station, set_suppression, is_marker_suppressed):
    current = 1.0
    previous_target = 1.0
    step = 0.0
    suppressed = False
    second_bytes = PCM_RATE * 4
    block_size = second_bytes * SEGMENT_SECONDS
    block = source.read(block_size)
    learned = np.zeros(FRAME_COUNT, dtype=bool)
    while len(block) >= 4:
        if len(block) == block_size and len(references):
            window = np.frombuffer(block, dtype="<i2").reshape(-1, 12)
            learned |= matched_frames(window.mean(axis=1, dtype=np.float32), references)
        segment = np.frombuffer(block[:min(second_bytes, len(block) // 4 * 4)], dtype="<i2").reshape(-1, 2)
        target = 1.0
        if len(segment) == PCM_RATE and gain < 1:
            samples = segment.reshape(-1, 6).mean(axis=1, dtype=np.float32)[:WINDOW_SAMPLES] / 32768
            try:
                with classifier_lock:
                    scores = summarize_scores(classifier.labels, classifier.predict(samples).tolist())
                decision = classify(scores, max(0.55, threshold))
                protected = scores.music >= 0.35 or scores.singing >= 0.20 or scores.jingle >= 0.35
                if not protected and (decision.category == "speech" or learned[:FRAMES_PER_SECOND].all()):
                    target = gain
            except (ValueError, RuntimeError):
                pass
        if gain < 1 and is_marker_suppressed and is_marker_suppressed(station):
            target = gain

        if target != previous_target:
            fade = fade_down if target < current else fade_up
            step = abs(target - current) / max(1, round(fade * PCM_RATE))
            previous_target = target
        if target != current:
            direction = 1 if target > current else -1
            distance = abs(target - current)
            movement = step * np.arange(1, len(segment) + 1)
            envelope = np.where(movement >= distance, target, current + direction * movement)
            current = float(envelope[-1])
            segment = (segment * envelope[:, None]).astype("<i2")
        elif current != 1:
            segment = (segment * current).astype("<i2")
        next_suppressed = target < 1 or current < 1
        if set_suppression and next_suppressed != suppressed:
            set_suppression(station, next_suppressed)
            suppressed = next_suppressed
        yield segment.tobytes()

        learned[:-FRAMES_PER_SECOND] = learned[FRAMES_PER_SECOND:]
        learned[-FRAMES_PER_SECOND:] = False
        block = block[second_bytes:] + source.read(second_bytes)
