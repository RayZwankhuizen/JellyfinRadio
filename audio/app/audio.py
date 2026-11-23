import shutil
import subprocess
import wave
from io import BytesIO
from pathlib import Path

import numpy as np

from .yamnet import SAMPLE_RATE


def read_audio(path: Path) -> np.ndarray:
    if path.suffix.lower() == ".wav":
        try:
            return _read_wave(path)
        except (wave.Error, EOFError):
            pass
    ffmpeg = shutil.which("ffmpeg")
    if ffmpeg is None:
        raise RuntimeError("FFmpeg is required to decode non-PCM WAV audio")
    result = subprocess.run(
        [ffmpeg, "-nostdin", "-v", "error", "-i", str(path), "-ac", "1", "-ar", str(SAMPLE_RATE), "-f", "f32le", "pipe:1"],
        check=True, capture_output=True,
    )
    return np.frombuffer(result.stdout, dtype="<f4").copy()


def read_audio_bytes(payload: bytes) -> np.ndarray:
    if len(payload) > 8 * 1024 * 1024:
        raise ValueError("audio upload exceeds 8 MiB")
    try:
        return _bound_analysis_audio(_read_wave_file(BytesIO(payload)))
    except (wave.Error, EOFError):
        ffmpeg = shutil.which("ffmpeg")
        if ffmpeg is None:
            raise RuntimeError("FFmpeg is required to decode this audio format")
        result = subprocess.run(
            [ffmpeg, "-nostdin", "-v", "error", "-i", "pipe:0", "-t", "300", "-ac", "1", "-ar", str(SAMPLE_RATE), "-f", "f32le", "pipe:1"],
            input=payload, check=True, capture_output=True,
        )
        return _bound_analysis_audio(np.frombuffer(result.stdout, dtype="<f4").copy())


def _bound_analysis_audio(samples: np.ndarray) -> np.ndarray:
    if samples.size == 0:
        raise ValueError("audio contains no samples")
    if samples.size > SAMPLE_RATE * 300:
        raise ValueError("audio analysis is limited to 5 minutes")
    return samples


def _read_wave(path: Path) -> np.ndarray:
    with wave.open(str(path), "rb") as audio:
        return _decode_wave(audio)


def _read_wave_file(file) -> np.ndarray:
    with wave.open(file, "rb") as audio:
        return _decode_wave(audio)


def _decode_wave(audio: wave.Wave_read) -> np.ndarray:
    if audio.getcomptype() != "NONE":
        raise wave.Error("compressed WAV")
    channels, width, rate = audio.getnchannels(), audio.getsampwidth(), audio.getframerate()
    raw = audio.readframes(audio.getnframes())
    if width == 1:
        data = (np.frombuffer(raw, np.uint8).astype(np.float32) - 128) / 128
    elif width == 2:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768
    elif width == 3:
        octets = np.frombuffer(raw, np.uint8).reshape(-1, 3).astype(np.int32)
        values = octets[:, 0] | (octets[:, 1] << 8) | (octets[:, 2] << 16)
        values = (values ^ 0x800000) - 0x800000
        data = values.astype(np.float32) / 8_388_608
    elif width == 4:
        data = np.frombuffer(raw, dtype="<i4").astype(np.float32) / 2_147_483_648
    else:
        raise wave.Error(f"unsupported PCM width: {width}")
    if channels > 1:
        data = data.reshape(-1, channels).mean(axis=1)
    if rate != SAMPLE_RATE:
        source_x = np.arange(data.size, dtype=np.float64)
        target_size = round(data.size * SAMPLE_RATE / rate)
        target_x = np.arange(target_size, dtype=np.float64) * rate / SAMPLE_RATE
        data = np.interp(target_x, source_x, data).astype(np.float32)
    return data
