# YAMNet model assets

`yamnet.tflite` is Google's float32 YAMNet 521-class audio classifier, obtained from the official [Google AI Edge model page](https://developers.google.com/edge/mediapipe/solutions/audio/audio_classifier). Its fixed input is 15,600 mono float samples at 16 kHz; outputs map to AudioSet classes.

`yamnet_class_map.csv` comes from the [TensorFlow Models YAMNet source](https://github.com/tensorflow/models/tree/master/research/audioset/yamnet). The model and source are Apache-2.0 licensed. The checksum is recorded in `yamnet.tflite.sha256`.

The model asset matches the checksum verified by the official MediaPipe sample: `4d8b4a53282dc83ef04e3e7dbc4fbc98082e34e44ed798e16c3a0cdd4c584faf`.
