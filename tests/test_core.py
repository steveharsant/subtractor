"""Unit tests for subtractor.core — all ffmpeg calls are mocked."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from unittest import mock

import pytest

from subtractor.core import (
    BITMAP_SUBTITLE_CODECS,
    TEXT_SUBTITLE_CODECS,
    ProbeResult,
    SubtitleStream,
    extract_all,
    extract_subtitle,
    find_ffmpeg_tool,
    get_output_path,
    is_text_subtitle,
    probe_video,
)


# -- Constants ---------------------------------------------------------------

VIDEO_PATH = Path("/videos/movie.mkv")
VIDEO_STEM = VIDEO_PATH.stem  # "movie"
VIDEO_PARENT = VIDEO_PATH.parent  # /videos


# -- Helper: build synthetic ffprobe JSON -----------------------------------


def _ffprobe_output(streams: list[dict] | None = None) -> str:
    return json.dumps({"streams": streams or [], "format": {"duration": "123.456"}})


# -- Test is_text_subtitle ---------------------------------------------------


class TestIsTextSubtitle:
    def test_subrip_is_text(self) -> None:
        assert is_text_subtitle("subrip")

    def test_ass_is_text(self) -> None:
        assert is_text_subtitle("ass")

    def test_ssa_is_text(self) -> None:
        assert is_text_subtitle("ssa")

    def test_webvtt_is_text(self) -> None:
        assert is_text_subtitle("webvtt")

    def test_mov_text_is_text(self) -> None:
        assert is_text_subtitle("mov_text")

    def test_text_is_text(self) -> None:
        assert is_text_subtitle("text")

    def test_dvd_subtitle_is_not_text(self) -> None:
        assert not is_text_subtitle("dvd_subtitle")

    def test_hdmv_pgs_subtitle_is_not_text(self) -> None:
        assert not is_text_subtitle("hdmv_pgs_subtitle")

    def test_xsub_is_not_text(self) -> None:
        assert not is_text_subtitle("xsub")

    def test_case_insensitive(self) -> None:
        assert is_text_subtitle("SubRip")
        assert is_text_subtitle("ASS")

    def test_unknown_codec_is_not_text(self) -> None:
        assert not is_text_subtitle("some_future_codec")

    def test_empty_string_is_not_text(self) -> None:
        assert not is_text_subtitle("")

    def test_text_codecs_is_subset_of_all_codecs(self) -> None:
        """Text + bitmap sets should not overlap."""
        assert TEXT_SUBTITLE_CODECS.isdisjoint(BITMAP_SUBTITLE_CODECS)


# -- Test get_output_path ----------------------------------------------------


class TestGetOutputPath:
    def test_single_stream_no_suffix(self) -> None:
        stream = SubtitleStream(index=0, codec="subrip", language="eng")
        result = get_output_path(VIDEO_PATH, stream, [stream])
        assert result == VIDEO_PARENT / f"{VIDEO_STEM}.txt"

    def test_single_stream_no_language(self) -> None:
        stream = SubtitleStream(index=0, codec="subrip")
        result = get_output_path(VIDEO_PATH, stream, [stream])
        assert result == VIDEO_PARENT / f"{VIDEO_STEM}.txt"

    def test_multiple_streams_with_language(self) -> None:
        eng = SubtitleStream(index=0, codec="subrip", language="eng")
        spa = SubtitleStream(index=1, codec="subrip", language="spa")
        all_streams = [eng, spa]
        assert get_output_path(VIDEO_PATH, eng, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.eng.txt"
        assert get_output_path(VIDEO_PATH, spa, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.spa.txt"

    def test_multiple_streams_no_language(self) -> None:
        s0 = SubtitleStream(index=0, codec="subrip")
        s1 = SubtitleStream(index=1, codec="ass")
        all_streams = [s0, s1]
        assert get_output_path(VIDEO_PATH, s0, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.0.txt"
        assert get_output_path(VIDEO_PATH, s1, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.1.txt"

    def test_language_collision_appends_index(self) -> None:
        a = SubtitleStream(index=2, codec="subrip", language="eng")
        b = SubtitleStream(index=5, codec="ass", language="eng")
        all_streams = [a, b]
        assert get_output_path(VIDEO_PATH, a, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.eng_2.txt"
        assert get_output_path(VIDEO_PATH, b, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.eng_5.txt"

    def test_three_same_language_streams(self) -> None:
        a = SubtitleStream(index=0, codec="subrip", language="jpn")
        b = SubtitleStream(index=3, codec="ass", language="jpn")
        c = SubtitleStream(index=4, codec="webvtt", language="jpn")
        all_streams = [a, b, c]
        # All three share "jpn", so each gets the collision suffix.
        assert get_output_path(VIDEO_PATH, a, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.jpn_0.txt"
        assert get_output_path(VIDEO_PATH, b, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.jpn_3.txt"
        assert get_output_path(VIDEO_PATH, c, all_streams) == VIDEO_PARENT / f"{VIDEO_STEM}.jpn_4.txt"

    def test_output_is_side_by_side(self) -> None:
        """Output files land in the same directory as the source video."""
        other_path = Path("/other/dir/video.mp4")
        stream = SubtitleStream(index=0, codec="subrip")
        result = get_output_path(other_path, stream, [stream])
        assert result.parent == Path("/other/dir")


# -- Test probe_video --------------------------------------------------------


class TestProbeVideo:
    def test_probe_returns_streams(self) -> None:
        ffprobe_stdout = _ffprobe_output(
            [
                {
                    "index": 0,
                    "codec_type": "subtitle",
                    "codec_name": "subrip",
                    "tags": {"language": "eng", "title": "English"},
                }
            ]
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert len(result.streams) == 1
                    s = result.streams[0]
                    assert s.index == 0
                    assert s.codec == "subrip"
                    assert s.language == "eng"
                    assert s.title == "English"
                    assert result.duration_seconds == 123.456

    def test_probe_filters_bitmap(self) -> None:
        ffprobe_stdout = _ffprobe_output(
            [
                {"index": 0, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle"},
                {"index": 1, "codec_type": "subtitle", "codec_name": "dvd_subtitle"},
            ]
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert len(result.streams) == 0

    def test_probe_mixed_text_and_bitmap(self) -> None:
        ffprobe_stdout = _ffprobe_output(
            [
                {"index": 0, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle"},
                {"index": 1, "codec_type": "subtitle", "codec_name": "subrip", "tags": {"language": "eng"}},
            ]
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert len(result.streams) == 1
                    assert result.streams[0].codec == "subrip"

    def test_probe_no_subtitle_streams(self) -> None:
        ffprobe_stdout = _ffprobe_output(
            [
                {"index": 0, "codec_type": "video", "codec_name": "h264"},
                {"index": 1, "codec_type": "audio", "codec_name": "aac"},
            ]
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert len(result.streams) == 0

    def test_probe_file_not_found(self) -> None:
        with mock.patch("subtractor.core.Path.is_file", return_value=False):
            with pytest.raises(FileNotFoundError):
                probe_video(VIDEO_PATH)

    def test_probe_ffprobe_not_found(self) -> None:
        with mock.patch("subtractor.core.find_ffmpeg_tool", side_effect=RuntimeError("not found")):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with pytest.raises(RuntimeError, match="not found"):
                    probe_video(VIDEO_PATH)

    def test_probe_ffprobe_error(self) -> None:
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.side_effect = subprocess.CalledProcessError(
                        returncode=1, cmd=["ffprobe"], stderr="unsupported codec"
                    )
                    with pytest.raises(RuntimeError, match="unsupported codec"):
                        probe_video(VIDEO_PATH)

    def test_probe_malformed_json(self) -> None:
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout="not json at all", stderr="", returncode=0
                    )
                    with pytest.raises(RuntimeError, match="Failed to parse ffprobe output"):
                        probe_video(VIDEO_PATH)

    def test_probe_streams_is_empty_tuple_when_no_text(self) -> None:
        """ProbeResult.streams is always a tuple, never None."""
        ffprobe_stdout = _ffprobe_output([])
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert isinstance(result.streams, tuple)
                    assert len(result.streams) == 0

    def test_probe_codec_without_name_skipped(self) -> None:
        ffprobe_stdout = _ffprobe_output(
            [{"index": 0, "codec_type": "subtitle"}]  # no codec_name
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert len(result.streams) == 0

    def test_probe_missing_tags(self) -> None:
        """Streams without a 'tags' key should work (language=None, title=None)."""
        ffprobe_stdout = _ffprobe_output(
            [{"index": 0, "codec_type": "subtitle", "codec_name": "subrip"}]
        )
        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffprobe"):
            with mock.patch("subtractor.core.Path.is_file", return_value=True):
                with mock.patch("subprocess.run") as mock_run:
                    mock_run.return_value = mock.Mock(
                        stdout=ffprobe_stdout, stderr="", returncode=0
                    )
                    result = probe_video(VIDEO_PATH)
                    assert result.streams[0].language is None
                    assert result.streams[0].title is None


# -- Test extract_subtitle ---------------------------------------------------


class TestExtractSubtitle:
    def test_extract_constructs_correct_command(self) -> None:
        stream = SubtitleStream(index=2, codec="subrip", language="eng")
        output = VIDEO_PARENT / f"{VIDEO_STEM}.eng.txt"

        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffmpeg"):
            with mock.patch("subprocess.run") as mock_run:
                mock_run.return_value = mock.Mock(stdout="", stderr="", returncode=0)
                extract_subtitle(VIDEO_PATH, stream, output)

        assert mock_run.call_count == 1
        cmd = mock_run.call_args[0][0]
        assert cmd[0] == "/usr/bin/ffmpeg"
        assert "-y" in cmd
        assert "-i" in cmd
        assert str(VIDEO_PATH) in cmd
        assert "-map" in cmd
        assert "0:s:2" in cmd
        assert "-f" in cmd
        assert "srt" in cmd
        assert str(output) in cmd

    def test_extract_ffmpeg_failure(self) -> None:
        stream = SubtitleStream(index=0, codec="subrip")
        output = VIDEO_PARENT / f"{VIDEO_STEM}.txt"

        with mock.patch("subtractor.core.find_ffmpeg_tool", return_value="/usr/bin/ffmpeg"):
            with mock.patch("subprocess.run") as mock_run:
                mock_run.side_effect = subprocess.CalledProcessError(
                    returncode=1, cmd=["ffmpeg"], stderr="permission denied"
                )
                with pytest.raises(RuntimeError, match="permission denied"):
                    extract_subtitle(VIDEO_PATH, stream, output)


# -- Test extract_all --------------------------------------------------------


class TestExtractAll:
    def _mock_probe(self, streams: list[SubtitleStream]) -> mock.Mock:
        return mock.Mock(streams=tuple(streams))

    def test_extract_all_single_file_success(self) -> None:
        """Happy path: one file, one subtitle stream."""
        stream = SubtitleStream(index=0, codec="subrip", language="eng")
        probe = ProbeResult(path=VIDEO_PATH, streams=(stream,), duration_seconds=100.0)

        with mock.patch("subtractor.core.probe_video", return_value=probe):
            with mock.patch("subtractor.core.extract_subtitle") as mock_extract:
                results = extract_all([VIDEO_PATH])

        assert mock_extract.call_count == 1
        assert VIDEO_PATH in results
        assert len(results[VIDEO_PATH]) == 1

    def test_extract_all_multiple_files(self) -> None:
        v1 = Path("/videos/a.mkv")
        v2 = Path("/videos/b.mp4")
        s1 = SubtitleStream(index=0, codec="subrip", language="eng")
        s2 = SubtitleStream(index=1, codec="ass", language="fre")

        probes = {
            v1: ProbeResult(path=v1, streams=(s1,)),
            v2: ProbeResult(path=v2, streams=(s2,)),
        }

        def fake_probe(p: Path) -> ProbeResult:
            return probes[p]

        with mock.patch("subtractor.core.probe_video", side_effect=fake_probe):
            with mock.patch("subtractor.core.extract_subtitle") as mock_extract:
                results = extract_all([v1, v2])

        assert mock_extract.call_count == 2
        assert v1 in results
        assert v2 in results

    def test_extract_all_callback_invocation(self) -> None:
        stream = SubtitleStream(index=0, codec="subrip")
        probe = ProbeResult(path=VIDEO_PATH, streams=(stream,))
        progress_calls: list[tuple[int, int]] = []
        status_calls: list[str] = []

        with mock.patch("subtractor.core.probe_video", return_value=probe):
            with mock.patch("subtractor.core.extract_subtitle"):
                extract_all(
                    [VIDEO_PATH],
                    progress_callback=lambda c, t: progress_calls.append((c, t)),
                    status_callback=lambda m: status_calls.append(m),
                )

        assert len(progress_calls) == 1
        assert progress_calls[0] == (1, 1)
        assert len(status_calls) >= 1
        assert any("Probing" in s for s in status_calls)

    def test_extract_all_skips_file_with_no_subtitles(self) -> None:
        probe = ProbeResult(path=VIDEO_PATH, streams=())
        progress_calls: list[tuple[int, int]] = []

        with mock.patch("subtractor.core.probe_video", return_value=probe):
            with mock.patch("subtractor.core.extract_subtitle") as mock_extract:
                results = extract_all(
                    [VIDEO_PATH],
                    progress_callback=lambda c, t: progress_calls.append((c, t)),
                )

        assert mock_extract.call_count == 0
        assert VIDEO_PATH not in results
        assert progress_calls == [(1, 1)]  # progress still advances

    def test_extract_all_continues_on_probe_error(self) -> None:
        v1 = Path("/videos/bad.mkv")
        v2 = Path("/videos/good.mp4")
        stream = SubtitleStream(index=0, codec="subrip")

        def fake_probe(p: Path) -> ProbeResult:
            if p == v1:
                raise RuntimeError("ffprobe crashed")
            return ProbeResult(path=p, streams=(stream,))

        with mock.patch("subtractor.core.probe_video", side_effect=fake_probe):
            with mock.patch("subtractor.core.extract_subtitle") as mock_extract:
                results = extract_all([v1, v2])

        assert mock_extract.call_count == 1
        assert v1 not in results
        assert v2 in results

    def test_extract_all_continues_on_extract_error(self) -> None:
        """If one stream fails, others in the same file should still be tried."""
        s0 = SubtitleStream(index=0, codec="subrip")
        s1 = SubtitleStream(index=1, codec="ass")
        probe = ProbeResult(path=VIDEO_PATH, streams=(s0, s1))

        def fake_extract(video_path: Path, stream: SubtitleStream, output_path: Path) -> None:
            if stream.index == 0:
                raise RuntimeError("extraction failed")

        with mock.patch("subtractor.core.probe_video", return_value=probe):
            with mock.patch("subtractor.core.extract_subtitle", side_effect=fake_extract):
                results = extract_all([VIDEO_PATH])

        assert VIDEO_PATH in results
        assert len(results[VIDEO_PATH]) == 1  # only stream #1 succeeded

    def test_extract_all_empty_input(self) -> None:
        results = extract_all([])
        assert results == {}

    def test_extract_all_file_not_found(self) -> None:
        with mock.patch("subtractor.core.probe_video", side_effect=FileNotFoundError("gone")):
            results = extract_all([VIDEO_PATH])
        assert VIDEO_PATH not in results


# -- Test find_ffmpeg_tool ---------------------------------------------------


class TestFindFfmpegTool:
    def test_finds_on_path(self) -> None:
        with mock.patch("shutil.which", return_value="/usr/bin/ffmpeg"):
            assert find_ffmpeg_tool("ffmpeg") == "/usr/bin/ffmpeg"

    def test_raises_when_not_found(self) -> None:
        with mock.patch("shutil.which", return_value=None):
            with pytest.raises(RuntimeError, match="not found on PATH"):
                find_ffmpeg_tool("ffmpeg")
