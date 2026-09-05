# Remote Whisper transcription

Select the `whisper` STT backend. This package sends 16 kHz, mono, 16-bit WAV
audio as a multipart request to an OpenAI-compatible transcription server.
It does not start or install that server.

| Environment variable | Meaning |
| --- | --- |
| `STT_HOST` | Custom server's absolute HTTP(S) origin, API base, or transcription URL. Examples: `http://stt.local:8000`, `http://stt.local:8000/v1`, or `http://stt.local:8000/v1/audio/transcriptions`. An existing `/v1` is not duplicated. Reverse-proxy prefixes are preserved. |
| `STT_MODEL` | Server model name; defaults to `whisper-1`. Set this to the name accepted by your local server. |
| `STT_KEY` | Optional bearer credential for a custom server. No Authorization header is sent when absent. |
| `STT_TIMEOUT` | Positive Go duration, default `30s`, covering the HTTP upload and response, e.g. `60s`. |
| `OPENAI_KEY` | Required only when `STT_HOST` is unset, which selects the official OpenAI endpoint. Never inherited by a custom server. |

Configure these in the service's environment outside source control. URLs must
include `http://` or `https://` and must not contain credentials, query strings,
or fragments. HTTP supports a trusted local network; use HTTPS where transport
confidentiality is required. Redirects are rejected rather than forwarding audio
or credentials. There are no automatic retries or cloud fallback.

The configured STT language (primary code, e.g. `de` from `de-DE`) and current
intent vocabulary prompt are preserved. The server must return JSON with a string
`text` field; an empty string is valid silence. HTTP failures, invalid/oversized
JSON, and canceled requests return errors without logging response bodies.
Cancellation follows the robot's gRPC stream context. EOF after collected audio
still transcribes that audio; an empty stream returns no transcription.

Local mock-server tests verify the contract. Actual transcription quality and
interoperability with a deployed Whisper service still require an audio test.
A language-model endpoint such as llama-swap's `/v1` is not evidence that a
`/v1/audio/transcriptions` service exists.
