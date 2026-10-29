# testdata

`tone.wav` is one second of a 440 Hz sine tone, 16 kHz mono 16-bit PCM, 32 044 bytes, which the
ffprobe integration tests probe through both runners. The host's ffmpeg n9.0.2 generated it, and
the command gives the same bytes on every run:

```sh
ffmpeg -nostdin -f lavfi -i sine=frequency=440:duration=1 -c:a pcm_s16le -ar 16000 -ac 1 \
  -fflags +bitexact -flags:a +bitexact -map_metadata -1 tone.wav
```

Its SHA-256 is `3be5f22b08cfb382561b033961f53b1fb4b40d297e29b2852be03b1e9b24b61f`.
