"""Minimal state facade for the guarded ToolHub execution module."""

__virtualname__ = "toolhub"


def __virtual__():
    return __virtualname__


def available(name):
    result = __salt__["test.version"]()
    return {"name": name, "result": bool(result), "changes": {}, "comment": "ToolHub Salt extension is available"}
