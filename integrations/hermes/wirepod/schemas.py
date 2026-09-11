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

VECTOR_OBSERVE = {
    "name": "vector_observe",
    "description": (
        "Read a live battery/charger observation and enrolled-face roster for this profile's assigned Vector. "
        "The roster is not a face-recognition event and must not be used to infer identity."
    ),
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
}

VECTOR_CAPTURE_IMAGE = {
    "name": "vector_capture_image",
    "description": (
        "Capture a fresh camera image from this profile's Vector and attach it to the current agent context. "
        "When a face or cliff event contains snapshot_id, pass it to retrieve that event-associated frame before it expires. "
        "Do not use an image to infer a person's identity."
    ),
    "parameters": {
        "type": "object",
        "properties": {"snapshot_id": {"type": "string", "pattern": "^[0-9a-f]{32}$"}},
        "additionalProperties": False,
    },
}

VECTOR_SAY = {
    "name": "vector_say",
    "description": (
        "Have this profile's Vector speak a short, user-safe sentence through its Vector voice. "
        "Use it immediately before any tool-using or physically consequential response to acknowledge the "
        "human while work begins, and when the human explicitly asks Vector to say, tell, or announce specific "
        "words. Do not use it merely to duplicate the ordinary final response."
    ),
    "parameters": {
        "type": "object",
        "properties": {"text": {"type": "string", "minLength": 1, "maxLength": 280}},
        "required": ["text"],
        "additionalProperties": False,
    },
}

VECTOR_DRIVE = {
    "name": "vector_drive",
    "description": (
        "Drive this Vector for one short bounded interval. Before wheel motion, use vector_observe and, when available, "
        "vector_capture_image to check the immediate route. For a direct ‘come here’ or ‘move closer’ request in the approved "
        "enclosed area, use a conservative short forward step toward the current facing direction, then re-observe. Motion stops "
        "automatically. This is wheel control, not autonomous navigation to a named location or person."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "left_wheel_mmps": {"type": "integer", "minimum": -200, "maximum": 200},
            "right_wheel_mmps": {"type": "integer", "minimum": -200, "maximum": 200},
            "duration_ms": {"type": "integer", "minimum": 50, "maximum": 2000},
        },
        "required": ["left_wheel_mmps", "right_wheel_mmps", "duration_ms"],
        "additionalProperties": False,
    },
}

def _joint_schema(name: str, subject: str) -> dict:
    return {
        "name": name,
        "description": f"Move this Vector's {subject} briefly; it stops automatically after the requested interval.",
        "parameters": {
            "type": "object",
            "properties": {
                "speed_rad_per_sec": {"type": "integer", "minimum": -2, "maximum": 2},
                "duration_ms": {"type": "integer", "minimum": 50, "maximum": 2000},
            },
            "required": ["speed_rad_per_sec", "duration_ms"],
            "additionalProperties": False,
        },
    }


VECTOR_MOVE_HEAD = _joint_schema("vector_move_head", "head")
VECTOR_MOVE_LIFT = _joint_schema("vector_move_lift", "lift")

VECTOR_STOP = {
    "name": "vector_stop",
    "description": "Immediately stop this Vector's wheels, head, and lift.",
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
}

VECTOR_UNDOCK = {
    "name": "vector_undock",
    "description": (
        "Ask this profile's Vector to leave its charger once. Use only in its approved enclosed play area; "
        "this is not permission for autonomous free-roam or navigation."
    ),
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
}

VECTOR_SCAN = {
    "name": "vector_scan",
    "description": (
        "Perform one stationary environmental scan with this profile's Vector. This does not drive, explore, "
        "or navigate the robot."
    ),
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
}
