//go:build linux

package audio

import (
	"regexp"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// FFmpegMinimum is the oldest FFmpeg the package accepts, for ffmpeg and ffprobe alike, which
// FFmpeg releases together: 9.0.
const FFmpegMinimum = "9.0"

// FFmpegImage is the default image ffmpeg and ffprobe run in through the container runner:
// linuxserver/ffmpeg's 9.0.2 CLI image, build ls89, pinned by its multi-arch index digest. Its
// own entrypoint is an init that changes users, which the runner's lockdown refuses; the runner
// makes the Spec's program the entrypoint in its place, so tool.Image{Ref: FFmpegImage} is the
// whole of it. A caller may run another image, or the tools on the host through the exec runner.
const FFmpegImage = "linuxserver/ffmpeg:9.0.2-cli-ls89@sha256:" +
	"822d04f0b2d0c4847430f5cc90f20ea48be866bb502ece63fdeb729400bf5361"

// FFmpegSpec declares FFmpeg's ffmpeg, minimum 9.0, for either runner's constructor. ffmpeg
// -version prints "ffmpeg version n9.0.2 ..." on a host whose build is tagged from git and
// "ffmpeg version 9.0.2 ..." in FFmpegImage; the pattern skips the n. A caller that needs another
// program path or a KillAfter period sets them on the Spec returned.
func FFmpegSpec() tool.Spec {
	return tool.Spec{
		Program:        "ffmpeg",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`ffmpeg version n?(\d+(?:\.\d+)*)`),
		Minimum:        FFmpegMinimum,
	}
}

// FFprobeSpec declares FFmpeg's ffprobe, minimum 9.0, for either runner's constructor; its
// version prints as ffmpeg's does, "ffprobe version n9.0.2 ..." or "ffprobe version 9.0.2 ...".
// A caller that needs another program path or a KillAfter period sets them on the Spec returned.
func FFprobeSpec() tool.Spec {
	return tool.Spec{
		Program:        "ffprobe",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`ffprobe version n?(\d+(?:\.\d+)*)`),
		Minimum:        FFmpegMinimum,
	}
}

// atLeast refuses t when its version is older than minimum, whatever minimum its own Spec
// declared, with a *tool.VersionError wrapping tool.ErrTooOld.
func atLeast(t *tool.Tool, minimum string) error {
	m, err := tool.ParseVersion(minimum)
	if err != nil {
		return err
	}
	if v := t.Version(); v.Compare(m) < 0 {
		return &tool.VersionError{Program: t.Name(), Found: v.String(), Minimum: minimum, Err: tool.ErrTooOld}
	}
	return nil
}
