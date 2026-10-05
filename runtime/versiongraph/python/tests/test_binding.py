"""The binding around the vectors: how a refused input and a bad call surface,
that the extension keeps no memory across calls, and that the core runs with
the GIL released."""

import json
import resource
import sys
import threading
import time
from typing import Any, List

import pytest

import superschematic_versiongraph as vg

DESCRIPTOR: vg.Descriptor = {
    "version": 3,
    "root": {"table": "recipe", "key": "id"},
    "refTable": "recipe_ref",
    "commitTable": "recipe_commit",
    "patchTable": "recipe_patch",
    "releaseTable": "recipe_release",
    "snapshotTable": "recipe_snapshot_entry",
    "kinds": [
        {
            "kind": "step",
            "table": "step",
            "historyTable": "step_history",
            "key": "entity_key",
            "id": "id",
            "ref": "ref",
            "tombstone": "deleted_on_ref",
            "version": "_version",
            "order": "position",
            "history": {"exclude": []},
            "columns": {
                "entity_key": "string",
                "id": "string",
                "ref": "string",
                "_version": "integer",
                "deleted_on_ref": "boolean",
                "position": "integer",
                "title": "string",
            },
        }
    ],
}


def rows(count: int, ref: str) -> List[vg.Row]:
    return [
        {
            "entity_key": f"step-{i:06d}",
            "id": f"{ref}-{i:06d}",
            "ref": ref,
            "_version": 1,
            "position": i,
            "title": f"Step {i}: " + "chop the onions finely " * 4,
        }
        for i in range(count)
    ]


def test_a_refused_input_raises_version_graph_error() -> None:
    with pytest.raises(vg.VersionGraphError) as caught:
        vg.validate({"descriptor": DESCRIPTOR, "tree": []})  # type: ignore[typeddict-item]
    error = caught.value
    assert error.code == "invalid_request"
    assert "a tree is a JSON object" in error.message
    assert str(error) == f"invalid_request: {error.message}"


def test_a_version_2_descriptor_is_invalid_descriptor() -> None:
    # The descriptor as version 2 wrote it, without each kind's history.
    v2: Any = json.loads(json.dumps(DESCRIPTOR))
    v2["version"] = 2
    for kind in v2["kinds"]:
        del kind["history"]
    with pytest.raises(vg.VersionGraphError) as caught:
        vg.validate({"descriptor": v2, "tree": {}})
    assert caught.value.code == "invalid_descriptor"
    assert "version 2 is not supported" in caught.value.message


def test_an_empty_document_is_invalid_json() -> None:
    with pytest.raises(vg.VersionGraphError) as caught:
        vg.run("diff", b"")
    assert caught.value.code == "invalid_json"


def test_run_takes_text_or_bytes_and_returns_the_output_text() -> None:
    document = json.dumps({"descriptor": DESCRIPTOR, "tree": {"step": rows(2, "r")}})
    from_text = vg.run("content_hash", document)
    from_bytes = vg.run("content_hash", document.encode("utf-8"))
    assert from_text == from_bytes
    assert json.loads(from_text) == vg.content_hash({"descriptor": DESCRIPTOR, "tree": {"step": rows(2, "r")}})


def test_an_unknown_operation_is_a_value_error() -> None:
    with pytest.raises(ValueError, match='unknown operation "rebase"'):
        vg.run("rebase", "{}")  # type: ignore[arg-type]


def test_the_codec_is_the_one_given() -> None:
    seen: List[str] = []

    def loads(text: str) -> Any:
        seen.append(text)
        return json.loads(text)

    graph = vg.VersionGraph(loads=loads, dumps=lambda value: json.dumps(value, sort_keys=True))
    result = graph.validate({"descriptor": DESCRIPTOR, "tree": {"step": rows(1, "r")}})
    assert result == {"findings": []}
    assert seen == ['{"findings":[]}']


def test_the_default_codec_refuses_nan() -> None:
    tree = {"step": [dict(rows(1, "r")[0], title=float("nan"))]}
    with pytest.raises(ValueError):
        vg.content_hash({"descriptor": DESCRIPTOR, "tree": tree})


def peak_rss_bytes() -> int:
    peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    # Bytes on macOS, kilobytes on Linux.
    return peak if sys.platform == "darwin" else peak * 1024


def test_many_calls_do_not_grow_the_process() -> None:
    # Each call sends about 650 KB and receives about 400 KB. A call that kept
    # its input or its output, in the extension or in the package, would grow
    # the peak by 160 MB or more over the loop; without a leak the peak stays
    # within a few MB.
    base = {"step": rows(2000, "base")}
    overlay = {"step": rows(2000, "overlay")[::2]}
    request = {"descriptor": DESCRIPTOR, "base": base, "overlay": overlay}
    first = vg.compose(request)
    for _ in range(20):
        vg.compose(request)
    before = peak_rss_bytes()
    for _ in range(400):
        assert vg.compose(request) == first
    grown = peak_rss_bytes() - before
    assert grown < 32 * 1024 * 1024, f"the peak grew by {grown} bytes over 400 calls"


def test_the_core_runs_with_the_gil_released() -> None:
    # One call on a large tree takes a while. While another thread runs it,
    # this thread keeps running Python: with the GIL held for the call, it
    # would stop for the call's whole length.
    request = json.dumps({"descriptor": DESCRIPTOR, "tree": {"step": rows(40000, "r")}}).encode("utf-8")
    started = time.perf_counter()
    vg.run("content_hash", request)
    length = time.perf_counter() - started
    assert length > 0.05, f"the call took {length:.3f}s, too short to measure"

    worker = threading.Thread(target=vg.run, args=("content_hash", request))
    # The first tick is before start, which returns only once the worker
    # runs and lets this thread have the GIL again.
    ticks = [time.perf_counter()]
    worker.start()
    while worker.is_alive():
        ticks.append(time.perf_counter())
    worker.join()
    gaps = [later - earlier for earlier, later in zip(ticks, ticks[1:])]
    assert gaps and max(gaps) < length / 2, f"this thread stopped for {max(gaps):.3f}s of a {length:.3f}s call"
