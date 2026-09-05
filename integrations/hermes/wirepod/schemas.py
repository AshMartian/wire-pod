"""Model-facing schemas for the wire-pod Hermes plugin."""

VECTOR_STATUS = {
    "name": "vector_status",
    "description": (
        "Get enrollment and activation status for this profile's assigned Vector. "
        "This is not a live connectivity check. This tool cannot control the robot "
        "or inspect any other Vector."
    ),
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
}
