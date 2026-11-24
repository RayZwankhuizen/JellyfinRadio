# Phase 2 work record

## Locked scope

- Preserve the existing Go importer, SQLite, M3U, proxy, UI and Docker setup.
- Evaluate classifiers against licensed, source-disjoint clips; no automatic gain changes until acceptance targets pass.
- Use Silero VAD only as one speech-evidence signal; do not add ASR or microphone capture.
- Keep speech/music separation opt-in and fail open to the original stream.
- Do not touch UbuntuMedia or the ARR stack.

## Progress

- [x] Inspect existing audio service, decision gate, stream proxy and UI.
- [x] Compare YAMNet, PANNs, task-specific classifiers, ONNX, Silero VAD and source separation.
- [x] Record acceptance targets and licensing/domain caveats.
- [x] Add a clip-level confusion-matrix evaluator and target-label manifest.
- [ ] Integrate a rights-cleared VAD model into the analysis ensemble. Silero VAD is documented but not installed or wired.
- [ ] Create the complete licensed test set and evaluate all acoustic classes. The manifest is a schema example; exploratory clips are outside the repo and one speech asset's rights are unverified.
- [x] Add per-station audio settings (API + UI) with analysis-only guard.
- [ ] Add realtime stream classification display; current UI displays uploaded-clip analysis only.
- [x] Add an opt-in manual-gain stream path from Go through FFmpeg with original passthrough by default.
- [ ] Complete automatic per-event gain control and seamless mid-session recovery; current manual route is a constant gain and falls back only before response output begins.
- [ ] Benchmark on UbuntuMedia hardware only after permission for a bounded read-only CPU run.

## Local validation

- Python audio tests: 19 passed in the built `smart-radio-audio` image with 0.5 CPU / 768 MiB limits.
- Go tests, `go vet ./...` and `go build ./...`: passed. The actual Go-to-FFmpeg stream integration test passed against a temporary localhost audio container.
- Compose config and both local image builds passed. End-to-end stream checks use synthetic audio only, not a real radio-station quality dataset.
- Web JS syntax, Python bytecode compile, and `git diff --check`: passed.
- Full licensed audio evaluation and production-hardware benchmark: not available from current fixtures/permissions.

## Current guard

The production playback filter remains disabled by default. When global DSP and a station's DSP setting are enabled, Arrow ICY markers can lower playback to the configured non-music volume until an `artist - track` title arrives. This marker rule has not been live-validated against the station; general classifier accuracy remains insufficient for broad news, ad, or speech filtering.
