# The extension module src/lib.rs builds.

def run(operation: str, input: bytes) -> tuple[bool, str]:
    """Runs one operation of the core on a JSON document: (True, output) with
    the operation's output document, or (False, output) with the core's
    {"error": {"code", "message"}} document. An unknown operation raises
    ValueError."""
