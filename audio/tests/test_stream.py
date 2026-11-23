import math
import struct
import threading
import unittest
import urllib.error
import urllib.request
import wave
import io
import http.client
import subprocess
from http.server import ThreadingHTTPServer

import numpy as np

from app.audio import read_audio_bytes
from app.learn import BAND_COUNT, FRAME_COUNT, _features
from app.non_music import PCM_RATE, buffered_audio, matched_frames
from app.service import Handler


class NonMusicBufferTests(unittest.TestCase):
    def test_consecutive_speech_stays_quiet_and_music_fades_back_up(self):
        class Classifier:
            labels = ["Speech", "Music", "Singing"]
            scores = iter([[1, 0, 0]] * 12 + [[1, 1, 0], [1, 0, 1], [0, 0, 0]])

            def predict(self, _samples):
                return np.asarray(next(self.scores))

        second = np.full((PCM_RATE, 2), 1000, dtype="<i2").tobytes()
        source = io.BytesIO(second * 15)
        result = list(buffered_audio(source, 0.2, classifier=Classifier(), classifier_lock=threading.Lock(),
                                     references=np.empty((0,)), threshold=0.8, fade_down=0, fade_up=1.5))
        self.assertEqual(len(result), 15)
        for block in result[:12]:
            self.assertTrue(np.all(np.frombuffer(block, dtype="<i2") == 200))
        fade = np.frombuffer(b"".join(result[12:]), dtype="<i2")[::2]
        self.assertTrue(np.all(np.diff(fade) >= 0))
        self.assertEqual(fade[0], 200)
        self.assertTrue(np.all(fade[PCM_RATE * 3 // 2:] == 1000))

    def test_approved_signature_matches_at_an_offset(self):
        mono = np.random.default_rng(42).integers(-3000, 3000, size=40000).astype(np.float32)
        fingerprint = _features(mono).reshape(FRAME_COUNT, BAND_COUNT)
        quantized = np.rint((fingerprint + 1) * 127.5) / 127.5 - 1
        offset = 8000
        shifted = np.concatenate((np.zeros(offset, dtype=np.float32), mono[:-offset]))
        matches = matched_frames(shifted, quantized[np.newaxis, :, :])
        self.assertTrue(matches[10:].all())
        self.assertFalse(matches[:10].any())


def tone_wav():
    output = io.BytesIO()
    with wave.open(output, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(16000)
        wav.writeframes(b"".join(struct.pack("<h", int(10000 * math.sin(2 * math.pi * 440 * i / 16000))) for i in range(16000)))
    return output.getvalue()


def tone_mp3():
    return subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "wav", "-i", "pipe:0", "-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "pipe:1"],
        input=tone_wav(), stdout=subprocess.PIPE, check=True,
    ).stdout


class StreamProcessingTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.server.daemon_threads = True
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.endpoint = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def test_process_endpoint_decodes_applies_gain_and_encodes_mp3(self):
        source = tone_mp3()
        request = urllib.request.Request(
            self.endpoint + "/process?gain=0.25", data=source,
            headers={"Content-Type": "audio/mpeg"}, method="POST",
        )
        with urllib.request.urlopen(request, timeout=20) as response:
            encoded = response.read()
            self.assertEqual(response.status, 200)
            self.assertEqual(response.headers.get_content_type(), "audio/mpeg")
            self.assertEqual(response.headers.get("X-Smart-Radio-Processor"), "ffmpeg-pcm-gain")

        original_samples = read_audio_bytes(source)
        processed_samples = read_audio_bytes(encoded)
        original_rms = float(np.sqrt(np.mean(np.square(original_samples))))
        processed_rms = float(np.sqrt(np.mean(np.square(processed_samples))))
        self.assertGreater(len(encoded), 1000)
        self.assertAlmostEqual(processed_rms / original_rms, 0.25, delta=0.04)

    def test_invalid_gain_is_rejected(self):
        request = urllib.request.Request(
            self.endpoint + "/process?gain=1.5", data=tone_wav(), method="POST",
        )
        with self.assertRaises(urllib.error.HTTPError) as error:
            urllib.request.urlopen(request, timeout=5)
        self.assertEqual(error.exception.code, 400)

    def test_chunked_input_returns_audio_before_the_request_finishes(self):
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=20)
        source = tone_mp3()
        connection.putrequest("POST", "/process?gain=0.5")
        connection.putheader("Transfer-Encoding", "chunked")
        connection.endheaders()
        connection.send(f"{len(source):X}\r\n".encode() + source + b"\r\n")
        response = connection.getresponse()
        self.assertEqual(response.status, 200)
        self.assertEqual(len(response.read(1024)), 1024)
        connection.close()


if __name__ == "__main__":
    unittest.main()
