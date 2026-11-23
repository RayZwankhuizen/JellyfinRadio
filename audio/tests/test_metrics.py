import unittest

from app.metrics import classification_report


class MetricsTests(unittest.TestCase):
    def test_reports_matrix_and_per_class_scores(self):
        report = classification_report(
            ["speech", "speech", "singing", "instrumental_music"],
            ["speech", "instrumental_music", "singing", "instrumental_music"],
            ["speech", "singing", "instrumental_music"],
            ["speech", "singing", "instrumental_music", "uncertain"],
        )
        self.assertEqual(report["confusion_matrix"]["speech"]["instrumental_music"], 1)
        self.assertEqual(report["per_class"]["speech"]["precision"], 1)
        self.assertEqual(report["per_class"]["speech"]["recall"], 0.5)
        self.assertAlmostEqual(report["per_class"]["speech"]["f1"], 2 / 3)
        self.assertEqual(report["per_class"]["uncertain"]["support"], 0)
        self.assertIsNone(report["per_class"]["uncertain"]["precision"])

    def test_unknown_labels_are_rejected(self):
        with self.assertRaises(ValueError):
            classification_report(["ad"], ["speech"], ["speech"], ["speech", "music"])


if __name__ == "__main__":
    unittest.main()
