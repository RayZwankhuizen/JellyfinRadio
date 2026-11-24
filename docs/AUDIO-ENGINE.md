# Audio analysis

The analysis service uses the official float32 YAMNet model through LiteRT's CPU backend. It reads mono 16 kHz audio in 975 ms windows with 487.5 ms hops and emits class scores with a conservative decision. Upload analysis never writes input audio to disk. During browser playback, live analysis runs on 975 ms frames; raw PCM is retained in SQLite only when the user explicitly assigns a label, together with model scores, for possible later training. `python -m app.analyze FILE` reports per-window decisions, CPU time and peak RSS.

The prior baseline used one speech sample, a CC0 female-vibrato singing sample, a CC0 piano sample, and speech mixed with that piano. In the resource-limited analyzer container (0.5 CPU, 768 MiB), measured per-window results were:

| Input | Windows | Decisions | CPU time | Peak RSS |
|---|---:|---|---:|---:|
| Speech sample | 8 | speech 8 | 0.0126 s | 68.3 MiB |
| Singing sample | 19 | singing 1, uncertain 18 | 0.0374 s | 80.7 MiB |
| Piano sample | 16 | music 14, uncertain 2 | 0.0253 s | 80.7 MiB |
| Speech over piano | 8 | music 8 | 0.0139 s | 80.7 MiB |

The measured window confusion matrix (rows are clip truth; columns are predicted category) was:

| Truth | Speech | Singing | Instrumental music | Uncertain |
|---|---:|---:|---:|---:|
| Speech (8 windows) | 8 | 0 | 0 | 0 |
| Singing (19 windows) | 0 | 1 | 0 | 18 |
| Instrumental music (16 windows) | 0 | 0 | 14 | 2 |
| Speech over music (8 windows) | 0 | 0 | 8 | 0 |

Across these 51 correlated windows, accuracy was 23/51 (45.1%). Per-class precision / recall / F1: speech 1.000 / 1.000 / 1.000; singing 1.000 / 0.053 / 0.100; instrumental music 0.636 / 0.875 / 0.737; speech-over-music undefined / 0 / 0 (no positive predictions). This is a tiny exploratory set, not an independent holdout; news, ad, and presenter have no evaluated examples. The former speech sample's reuse rights were not verified in this checkout, so it is not bundled as a project dataset.

The local WSL Docker image was rebuilt and exercised with a 2-second synthetic 440 Hz tone. Model startup/inference completed in 6.3 ms wall / 6.6 ms process CPU, with 68,880 KiB peak RSS, four analysis windows, and unity target gain. This only confirms runtime wiring and resource shape for a synthetic tone; it says nothing about class accuracy. These numbers are on the WSL development host, not the UbuntuMedia Athlon.

Every result remained analysis-only with target gain 1.0. The speech-over-music classifier missed speech in all eight windows; it did not establish a safe ducking decision. Singing recall was poor. This is not sufficient for any automatic filter. Synthetic-score unit tests separately confirm that a confidently detected singing window resets the speech gate and that mixed audio remains at unity gain unless explicitly enabled.

The container run used the same 0.5 CPU / 768 MiB limits as production Compose. Peak RSS is the process high-water mark across cases, including model startup, not incremental per-clip memory. A separate WSL run measured about 12 ms CPU for speech and 54 ms for singing, with 61 MiB and 66 MiB peak RSS. These are development-host measurements, not an UbuntuMedia benchmark. The low inference cost does not solve the classification errors.

## Safety and limitations

- The raw Go proxy remains the default. With global DSP and a station's DSP setting enabled, the non-music path can lower volume for classifier detections and for configured ICY title markers. Classifier accuracy is not validated; Arrow marker suppression is specific to `ARROW - CLASSIC ROCK` and `HET ARROW CLASSIC ROCK NIEUWS` titles and resumes on an `artist - track` title.
- The opt-in route pipes the original HTTP response to the private audio service using chunked HTTP, decodes it with FFmpeg, applies a constant PCM-domain volume filter, and re-encodes as 128 kbps MP3. It uses bounded pipes, one concurrent DSP session, 8 KiB probing, one FFmpeg thread, and no audio files on disk. Original passthrough is selected when DSP is disabled, busy, sees an HLS playlist, or fails before output headers. A failure after output starts closes that session; changing from MP3 back to a possibly different upstream codec mid-response is unsafe.
- MP3 output does not preserve upstream ICY metadata. The DSP path has a fixed output codec/bitrate and fades toward the configured target volume; upstream ICY metadata drives the Arrow marker state through a separate control request.
- The proxy requests upstream ICY metadata, removes those blocks from audio bytes, and exposes the latest title (with a best-effort `artist - track` split) through the station metadata API. The browser audio panel shows that title and the station source URL with a copy action. Streams without ICY metadata cannot provide artist/title this way.
- Live analysis displays a compact, non-scrolling frequency spectrum and horizontal mixer meters for bass, mids, treble and classifier scores. Jingle is available as a classifier label and explicit feedback label; automatic filtering is still disabled.
- A global non-music target volume is stored in `user_settings` and can be set from Audioanalyse. It is not applied to playback yet; classifier-to-stream gain control remains disabled pending reliable validation.
- Station learning allows one session at a time. The four-hour repeated-audio mode compares five-second mono PCM blocks using cosine similarity over normalized spectral features; it creates a pending candidate only above 80% similarity and when the match is at least 60 seconds later. ICY track metadata is recorded for both matching blocks, and matches within the same named track are skipped. The one-hour transition mode runs YAMNet on one-second mono PCM windows, confirms a new category after two consecutive windows, and stores a ten-second WAV clip centered on the first changed window (five seconds before and after). Streams without track metadata can still produce song-fragment candidates in repeated-audio mode. SQLite stores the match signature or transition categories and one preview per candidate; it does not store the continuous stream. User review can be undone; approval does not trigger playback filtering.
- Full-stream ducking can lower clear speech-only windows while leaving uncertain and singing windows at unity, but only after per-station validation and explicit enablement.
- YAMNet is a sound-event classifier, not a speech/music source separator. On the tested speech-over-piano mix it classified every window as music. Lowering that stream would still lower both voice and accompaniment. The implementation leaves mixed windows unchanged by default. Source separation is not included because a suitable low-CPU quality/latency result has not been established for the UbuntuMedia Athlon 3050U.
- Singing can be mistaken for speech by sound-event classifiers. The current YAMNet mapping is too weak to enable automatic ducking globally. Evaluate with station recordings before enabling any station profile.
- The FFmpeg manual-gain route is connected to the Go `/stream/{station_id}` proxy and can be tested from a Jellyfin M3U station when both global and per-station flags are enabled. The management page still shows uploaded-clip analysis, not live classifications. Silero VAD is documented but is not wired; no automatic filtering is exposed.
- The Python suite passes in the pinned audio container (19 tests), including manifest/source-split validation, analysis-only unity gain, chunked live input, actual MP3 output and measured gain. Go tests, `go vet` and build pass; the optional end-to-end test sent synthetic audio through the Go proxy and real FFmpeg container and received MP3 output. Local Compose config and both images build. These tests do not validate actual Radio Browser sources, classifier quality, or UbuntuMedia performance.

## Repeat the checks

```sh
cd audio
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python -m unittest discover -s tests -v
PYTHONPATH=. .venv/bin/python -m app.analyze /path/to/clip.wav
```

For the full Docker path, start `make dev` and place license-verified clips named `speech.wav`, `singing.wav` and `music.ogg` in `/tmp/jfr-fixtures`. Then run the script in a disposable peer on the Compose network (replace `deploy_default` if your Compose project uses another network name):

```sh
docker run --rm --network deploy_default --cpus=0.5 --memory=768m \
  --read-only --tmpfs /tmp:rw,size=64m,uid=10001,gid=10001 \
  --mount type=bind,source=/tmp/jfr-fixtures,target=/samples,readonly \
  --mount type=bind,source="$PWD/scripts/test-audio-container.py",target=/test.py,readonly \
  jellyfin-smart-radio-audio:dev python /test.py /samples http://smart-radio:8787
```

It creates the speech/music mix in a disposable `/tmp` directory, POSTs all four cases through the Go API, and asserts that every target gain stays at unity.

Model documentation: [Google AI Edge Audio Classifier](https://developers.google.com/edge/mediapipe/solutions/audio/audio_classifier). Sample speech clip: [MediaPipe sample asset](https://storage.googleapis.com/mediapipe-assets/speech_16000_hz_mono.wav). Singing sample: [Wikimedia Commons CC0 file](https://commons.wikimedia.org/wiki/File:Female-vibrato-1.wav). Instrumental sample: [Wikimedia Commons CC0 piano](https://commons.wikimedia.org/wiki/File:Eb_major_piano.ogg).
