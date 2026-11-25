"""Convert audio files to Ogg Opus, the format WhatsApp voice messages need."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile


def convert_to_opus_ogg(input_file: str, output_file: str | None = None, bitrate: str = "32k", sample_rate: int = 24000) -> str:
    """Convert ``input_file`` to Opus in an Ogg container and return the output path.

    Raises:
        FileNotFoundError: the input file does not exist
        RuntimeError: ffmpeg is missing or the conversion failed
    """
    if not os.path.isfile(input_file):
        raise FileNotFoundError(f"Input file not found: {input_file}")
    if shutil.which("ffmpeg") is None:
        raise RuntimeError("ffmpeg is not installed or not on PATH (install it, or send the file with send_file instead)")
    if output_file is None:
        output_file = os.path.splitext(input_file)[0] + ".ogg"
    out_dir = os.path.dirname(output_file)
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)

    cmd = [
        "ffmpeg", "-i", input_file,
        "-c:a", "libopus", "-b:a", bitrate, "-ar", str(sample_rate),
        "-application", "voip", "-vbr", "on", "-compression_level", "10", "-frame_duration", "60",
        "-y", output_file,
    ]  # fmt: skip
    try:
        subprocess.run(cmd, capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as exc:
        raise RuntimeError(f"ffmpeg failed to convert the audio: {exc.stderr.strip()[-500:]}") from exc
    return output_file


def convert_to_opus_ogg_temp(input_file: str, bitrate: str = "32k", sample_rate: int = 24000) -> str:
    """Like :func:`convert_to_opus_ogg`, but writes to a new temporary file."""
    fd, temp_path = tempfile.mkstemp(suffix=".ogg")
    os.close(fd)
    try:
        return convert_to_opus_ogg(input_file, temp_path, bitrate, sample_rate)
    except Exception:
        if os.path.exists(temp_path):
            os.unlink(temp_path)
        raise


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python audio.py input_file")
        sys.exit(1)
    try:
        print(f"Successfully converted to: {convert_to_opus_ogg_temp(sys.argv[1])}")
    except Exception as exc:
        print(f"Error: {exc}")
        sys.exit(1)
