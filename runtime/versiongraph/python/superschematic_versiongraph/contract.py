"""The JSON contract of the version-graph core (runtime/versiongraph/README.md)
as Python types. Every member is named exactly as the contract names it.

Nothing checks these types at run time. The vector suite
(tests/test_vectors.py) keeps them honest: it checks every vector's input and
output against them, member by member and literal by literal, and fails when
a member or literal here appears in no vector. So a member or a literal
missing, extra or misnamed here fails it.

The types are for Python 3.9 and newer: a TypedDict's optional members are
declared on a ``total=False`` subclass of one holding the required ones.
"""

from typing import Any, Dict, List, Literal, TypedDict, Union

__all__ = [
    "Unit",
    "ElementClass",
    "ValueClass",
    "ParentEdge",
    "KindHistory",
    "KindDescriptor",
    "RootTable",
    "Descriptor",
    "Row",
    "Tree",
    "FindingCode",
    "Finding",
    "ComposeInput",
    "ComposeOutput",
    "Take",
    "TakeResolution",
    "ValueResolution",
    "Resolution",
    "MergeInput",
    "Conflict",
    "Side",
    "EntityOutcome",
    "MergeOutput",
    "DiffInput",
    "ChangeOperation",
    "Change",
    "DiffOutput",
    "TreeInput",
    "ContentHashOutput",
    "ValidateOutput",
    "OperationName",
    "ErrorCode",
    "ErrorBody",
    "ErrorDocument",
]

Unit = Literal["atomic", "keyed", "jsonSchema"]
"""How a content column merges."""

ElementClass = Literal[
    "string",
    "integer",
    "number",
    "boolean",
    "uuid",
    "dateTime",
    "date",
    "time",
    "duration",
    "enum",
    "json",
]
"""The class of one value: what the schema runtime's JSON for a field's type distinguishes."""

ValueClass = Literal[
    "string",
    "string[]",
    "string[][]",
    "integer",
    "integer[]",
    "integer[][]",
    "number",
    "number[]",
    "number[][]",
    "boolean",
    "boolean[]",
    "boolean[][]",
    "uuid",
    "uuid[]",
    "uuid[][]",
    "dateTime",
    "dateTime[]",
    "dateTime[][]",
    "date",
    "date[]",
    "date[][]",
    "time",
    "time[]",
    "time[][]",
    "duration",
    "duration[]",
    "duration[][]",
    "enum",
    "enum[]",
    "enum[][]",
    "json",
    "json[]",
    "json[][]",
]
"""A column's value class: an element class, a list of one (``[]``) or a list
of lists (``[][]``). A storage adapter normalizes the column by it
(runtime/versiongraph/README.md, "Canonical rows")."""


class ParentEdge(TypedDict):
    """A kind's containment edge."""

    key: str
    """The column holding the parent row's entity key (a string, or null for none)."""
    kind: str
    """The parent's kind, which may be the kind itself."""


class _KindHistoryRequired(TypedDict):
    exclude: List[str]
    """The columns every history image leaves out: none of them content, nor a
    role column other than the author."""


class KindHistory(_KindHistoryRequired, total=False):
    """What a kind's history keeps: the facts the sql generator's triggers and
    prune function hold, for a storage adapter that writes history itself. The
    core checks it against the kind's columns and does not read it otherwise."""

    retentionDays: int
    """How many days of history pruning keeps, a positive integer; absent for
    no retention."""
    actor: str
    """The column a delete's image names its actor in, which history keeps;
    absent for none."""


class _KindDescriptorRequired(TypedDict):
    kind: str
    """The kind's name: the tree member that holds its rows. Unique."""
    table: str
    """The table that holds the kind's rows."""
    historyTable: str
    """The table that holds the kind's history images."""
    key: str
    """The entity key column: the logical identity rows are matched on."""
    id: str
    """The row id column."""
    ref: str
    """The column naming the ref the row was written on."""
    tombstone: str
    """A boolean column; true marks the row as the entity's delete."""
    version: str
    """The row version column."""
    history: KindHistory
    """What the kind's history keeps."""
    columns: Dict[str, ValueClass]
    """Every column of the kind's table, with its value class."""


class KindDescriptor(_KindDescriptorRequired, total=False):
    """Which column of a kind's rows plays which role, and how content merges."""

    root: str
    """The column holding the graph root's key, which a storage adapter writes
    on every row. The core does not read it."""
    author: str
    """The column naming who wrote the row; conflicts report it."""
    parent: ParentEdge
    order: str
    """An integer column that orders siblings."""
    singleton: bool
    """At most one live row. Default false."""
    units: Dict[str, Unit]
    """A conflict unit per content column; atomic when absent."""
    excluded: List[str]
    """Columns that are not content."""


class RootTable(TypedDict):
    """The graph root's table and its key column."""

    table: str
    key: str


class _DescriptorRequired(TypedDict):
    version: Literal[3]
    """The descriptor format; the core reads version 3 and refuses any other."""
    root: RootTable
    refTable: str
    commitTable: str
    patchTable: str
    releaseTable: str
    snapshotTable: str
    kinds: List[KindDescriptor]


class Descriptor(_DescriptorRequired, total=False):
    """The graph descriptor the ORM generator writes as versiongraph/<name>.json.

    ``refTable``, ``commitTable``, ``patchTable``, ``releaseTable`` and
    ``snapshotTable`` name the tables of the graph's refs, commits, patches,
    release pointers and snapshot entries.
    """

    graph: str
    """The graph's name; the core does not read it."""


Row = Dict[str, Any]
"""A row keyed by column name: a canonical row once a storage adapter has
normalized it. Values are whatever the configured JSON decoder gives (see
``VersionGraph``)."""

Tree = Dict[str, List[Row]]
"""``{"<kind>": [row, ...]}``. A missing kind has no rows."""

FindingCode = Literal[
    "absent_parent",
    "duplicate_entity_key",
    "singleton",
    "parent_cycle",
    "order_out_of_range",
]


class _FindingRequired(TypedDict):
    code: FindingCode
    kind: str
    message: str


class Finding(_FindingRequired, total=False):
    """A problem in a tree that compose reports and validate lists."""

    entityKey: str
    """Absent for a finding about a whole kind (``singleton``)."""


class ComposeInput(TypedDict):
    descriptor: Descriptor
    base: Tree
    overlay: Tree


class ComposeOutput(TypedDict):
    tree: Tree
    """Every kind of the descriptor, with no tombstones."""
    findings: List[Finding]


Take = Literal["base", "ours", "theirs"]
"""The input a resolution takes a unit's value from."""


class TakeResolution(TypedDict):
    """Settles one conflict by taking the unit from one input."""

    kind: str
    entityKey: str
    path: str
    take: Take


class ValueResolution(TypedDict):
    """Settles one conflict by giving the unit's value (None, JSON null, is a value)."""

    kind: str
    entityKey: str
    path: str
    value: Any


Resolution = Union[TakeResolution, ValueResolution]


class _MergeInputRequired(TypedDict):
    descriptor: Descriptor
    base: Tree
    ours: Tree
    theirs: Tree


class MergeInput(_MergeInputRequired, total=False):
    resolutions: List[Resolution]


class _ConflictRequired(TypedDict):
    kind: str
    entityKey: str
    path: str
    """A JSON Pointer into the row."""


class Conflict(_ConflictRequired, total=False):
    """A unit both sides changed differently, or an edit against a delete
    (path ""). ``base``, ``ours`` and ``theirs`` are the unit's values, absent
    where the unit is absent; for path "" they are whole rows, absent on a
    deleted side."""

    base: Any
    ours: Any
    theirs: Any
    oursAuthor: Any
    theirsAuthor: Any


Side = Literal["ours", "theirs", "merged", "conflict"]


class _EntityOutcomeRequired(TypedDict):
    kind: str
    entityKey: str
    side: Side


class EntityOutcome(_EntityOutcomeRequired, total=False):
    """Where an entity's merged result came from."""

    deleted: bool
    """True when the result is a delete."""


class MergeOutput(TypedDict):
    merged: Tree
    """Every settled entity's result; apply it only when ``conflicts`` is empty."""
    conflicts: List[Conflict]
    entities: List[EntityOutcome]


# "from" is a keyword, so DiffInput takes the functional form.
DiffInput = TypedDict("DiffInput", {"descriptor": Descriptor, "from": Tree, "to": Tree})

ChangeOperation = Literal["ADD", "UPDATE", "DELETE"]


class _ChangeRequired(TypedDict):
    kind: str
    entityKey: str
    operation: ChangeOperation


class Change(_ChangeRequired, total=False):
    row: Row
    """``to``'s row: its tombstone for a DELETE, when ``to`` has one."""


class DiffOutput(TypedDict):
    changes: List[Change]


class TreeInput(TypedDict):
    """The input of content_hash and validate."""

    descriptor: Descriptor
    tree: Tree


class ContentHashOutput(TypedDict):
    contentHash: str
    """SHA-256, as lowercase hex, of the tree's canonical content."""


class ValidateOutput(TypedDict):
    findings: List[Finding]
    """Empty for a valid tree."""


OperationName = Literal["compose", "merge", "diff", "content_hash", "validate"]
"""The core's operations, as the vectors and the C ABI (``vg_<op>``) name them."""

ErrorCode = Literal[
    "invalid_json",
    "invalid_request",
    "invalid_descriptor",
    "unknown_kind",
    "invalid_row",
    "duplicate_entity_key",
    "order_out_of_range",
    "invalid_resolution",
    "unmatched_resolution",
    "internal",
]


class ErrorBody(TypedDict):
    code: ErrorCode
    message: str


class ErrorDocument(TypedDict):
    """What the core returns for a refused input."""

    error: ErrorBody
