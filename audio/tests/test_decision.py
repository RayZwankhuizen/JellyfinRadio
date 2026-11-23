import unittest

from app.decision import Ducker, GainRamp, Scores, SpeechGate, classify


class DecisionTests(unittest.TestCase):
    def test_singing_is_never_classified_as_duckable_speech(self):
        result = classify(Scores(speech=0.72, singing=0.48, music=0.84))
        self.assertEqual(result.category, "singing")

    def test_music_and_speech_over_music_are_distinct(self):
        self.assertEqual(classify(Scores(music=0.9)).category, "music")
        self.assertEqual(classify(Scores(speech=0.8, music=0.7)).category, "speech_over_music")

    def test_analysis_only_and_disabled_modes_keep_unity_gain(self):
        speech = Scores(speech=0.95)
        for ducker in (Ducker(), Ducker(analysis_only=False, enabled=False)):
            _, gain = ducker.update(speech, 0.5)
            _, gain = ducker.update(speech, 0.5)
            self.assertEqual(gain, 1.0)

    def test_mixed_stream_is_not_ducked_without_explicit_opt_in(self):
        ducker = Ducker(analysis_only=False, enabled=True)
        for _ in range(3):
            decision, gain = ducker.update(Scores(speech=0.95, music=0.8), 0.5)
        self.assertEqual(decision.category, "speech_over_music")
        self.assertEqual(gain, 1.0)

    def test_gate_uses_hysteresis_and_singing_override(self):
        gate = SpeechGate(enter=0.75, exit=0.5, attack_windows=2, release_windows=2)
        speech = Scores(speech=0.8)
        self.assertFalse(gate.update(speech))
        self.assertTrue(gate.update(speech))
        self.assertTrue(gate.update(Scores(speech=0.6)))
        self.assertTrue(gate.update(Scores(speech=0.1)))
        self.assertFalse(gate.update(Scores(speech=0.1)))
        self.assertFalse(gate.update(Scores(speech=0.9, singing=0.7)))

    def test_gain_ramp_takes_configured_time(self):
        ramp = GainRamp(fade_seconds=1.5)
        self.assertAlmostEqual(ramp.update(0.2, 0.75), 0.6)
        self.assertAlmostEqual(ramp.update(0.2, 0.75), 0.2)
        self.assertAlmostEqual(ramp.update(1.0, 1.5), 1.0)


if __name__ == "__main__":
    unittest.main()
