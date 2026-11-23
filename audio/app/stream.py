import os
import select
import shutil
import subprocess
import threading
from urllib.parse import parse_qs, urlsplit


def _chunks(handler):
    transfer = handler.headers.get("Transfer-Encoding", "").lower()
    if "chunked" in transfer:
        while True:
            line = handler.rfile.readline(128)
            if not line.endswith(b"\n"):
                raise ValueError("invalid chunk framing")
            try:
                size = int(line.split(b";", 1)[0].strip(), 16)
            except ValueError as error:
                raise ValueError("invalid chunk size") from error
            if size == 0:
                while handler.rfile.readline(8192) not in (b"\r\n", b"\n", b""):
                    pass
                return
            remaining = size
            while remaining:
                block = handler.rfile.read(min(64 * 1024, remaining))
                if not block:
                    raise ValueError("truncated request body")
                remaining -= len(block)
                yield block
            if handler.rfile.read(2) != b"\r\n":
                raise ValueError("invalid chunk terminator")
        return

    length = int(handler.headers.get("Content-Length", "-1"))
    if length < 0:
        raise ValueError("content-length or chunked transfer encoding required")
    remaining = length
    while remaining:
        block = handler.rfile.read(min(64 * 1024, remaining))
        if not block:
            raise ValueError("truncated request body")
        remaining -= len(block)
        yield block


def process(handler, buffered_audio=None):
    params = parse_qs(urlsplit(handler.path).query)
    try:
        gain = float(params.get("gain", ["1"])[0])
    except ValueError:
        handler.send_error(400, "invalid gain")
        return
    if not 0 <= gain <= 1:
        handler.send_error(400, "gain must be between 0 and 1")
        return

    ffmpeg = os.getenv("FFMPEG_BIN") or shutil.which("ffmpeg")
    if not ffmpeg:
        handler.send_error(503, "FFmpeg unavailable")
        return
    command = [
        ffmpeg, "-hide_banner", "-loglevel", "error", "-nostats", "-nostdin",
        "-threads", "1", "-probesize", "8192", "-analyzeduration", "0", "-i", "pipe:0",
        "-map", "0:a:0", "-vn", "-af", f"volume={gain:.6f}:eval=frame",
        "-c:a", "libmp3lame", "-threads", "1", "-b:a", "128k", "-ar", "44100", "-ac", "2",
        "-flush_packets", "1", "-f", "mp3", "pipe:1",
    ]
    decoder = None
    if buffered_audio is not None:
        decoder_command = command[:command.index("-af")] + ["-ar", "48000", "-ac", "2", "-f", "s16le", "pipe:1"]
        command = command[:command.index("-probesize")] + [
            "-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:0",
        ] + command[command.index("-c:a"):]
    try:
        proc = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=8192)
        if buffered_audio is not None:
            decoder = subprocess.Popen(decoder_command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError:
        if "proc" in locals():
            _stop(proc)
            proc.stdin.close()
            proc.stdout.close()
        handler.send_error(503, "could not start FFmpeg")
        return

    def feed():
        target = decoder if decoder is not None else proc
        try:
            for block in _chunks(handler):
                target.stdin.write(block)
                target.stdin.flush()
        except (BrokenPipeError, ConnectionError, OSError, ValueError):
            pass
        finally:
            try:
                target.stdin.close()
            except OSError:
                pass

    feeder = threading.Thread(target=feed, daemon=True)
    feeder.start()
    processor = None
    if decoder is not None:
        def transform():
            try:
                for block in buffered_audio(decoder.stdout, gain):
                    proc.stdin.write(block)
                    proc.stdin.flush()
            except (BrokenPipeError, ConnectionError, OSError):
                pass
            finally:
                proc.stdin.close()

        processor = threading.Thread(target=transform, daemon=True)
        processor.start()
    try:
        ready, _, _ = select.select([proc.stdout], [], [], 15)
        first = proc.stdout.read1(8192) if ready else b""
        if not first:
            handler.send_error(502 if proc.poll() is not None else 504, "FFmpeg produced no audio")
            return
        handler.send_response(200)
        handler.send_header("Content-Type", "audio/mpeg")
        handler.send_header("Cache-Control", "no-store")
        handler.send_header("X-Accel-Buffering", "no")
        handler.send_header("X-Smart-Radio-Processor", "ffmpeg-pcm-non-music" if decoder is not None else "ffmpeg-pcm-gain")
        handler.end_headers()
        handler.wfile.write(first)
        handler.wfile.flush()
        while True:
            block = proc.stdout.read1(8192)
            if not block:
                break
            handler.wfile.write(block)
            handler.wfile.flush()
    except (BrokenPipeError, ConnectionError, OSError):
        pass
    finally:
        _stop(proc)
        if decoder is not None:
            _stop(decoder)
            processor.join(timeout=2)
            if not processor.is_alive():
                decoder.stdout.close()
        feeder.join(timeout=2)
        proc.stdout.close()


def _stop(proc):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
