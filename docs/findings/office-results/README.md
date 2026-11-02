# Results behind the office findings

Each file holds one integration-tier test's `-v` output, verbatim, under a header that gives the
run's date, host, image digest, and tool versions. All three are from one run of
`mise exec -- env GOWORK=off go test -race -tags integration -count=1 -v ./office/` on
2026-11-02, from the laptop (Linux 7.2.15 x86_64, docker server 29.9.2), with office 0.1.0 from
the mise pin, soffice 26.8.1.1 in `LibreOfficeImage`
(`gotenberg/gotenberg:8.39.0-libreoffice@sha256:4c0eb92ef22e5d5fb0a49eac924b63d85d0b0334ea3365a1f5ca428f367b3054`),
and ImageMagick 7.1.2-34 on the host. The vision requests went to gb10-2's llama.cpp router,
build b11669 (`b11669-620d2225c`), serving set A. [`../office.md`](../office.md) cites them.

| File | Test | What it records |
|---|---|---|
| `legacy-round-trip.log` | `TestLegacyRoundTrip` | Each legacy fixture converted twice through the default image: its first and repeat wall time (`footprint:`), the image's compressed and on-disk size, and its loss per element kind (`loss:`), each lost and added key, and the totals per format for the generated set |
| `pictures.log` | `TestPictures` | Each EMF, WMF, and TIFF picture converted to PNG at the default scale: stored size, the PNG's dimensions and size, and the picture-conversion time of its document |
| `vision.log` | `TestVisionPictures` | Each of the 16 distinct pictures handed onward, sent once to Gemma 4 26B-A4B (`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`): its wall time, image tokens, output tokens, and the description it returned |

The image was already present on the laptop, so the run timed no pull.
