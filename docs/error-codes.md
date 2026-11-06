# Error codes

Every error code that can reach a client through the Host API is listed here, either as public
(clients may rely on it) or as hidden on purpose (clients see only a generic failure), with a
reason. `TestErrorCodesPublished` fails when `internal/platform/errcode` declares a `Code`
constant that is in neither list, in both, or when this page names a code that does not exist.

Changing this file needs a review of what clients can rely on; hiding a code needs a reason.

## Public

| Code | Meaning |
| --- | --- |
| `invalid_argument` | The request is malformed or a value is out of range. |
| `not_found` | The named thing does not exist. |
| `already_exists` | The thing to create already exists. |
| `failed_precondition` | The system is not in a state that allows the operation. |
| `permission_denied` | The caller is not allowed to do this. |
| `unavailable` | A dependency is temporarily out of reach; retrying may help. |
| `canceled` | The operation was canceled by its caller. |
| `deadline_exceeded` | The operation took longer than its deadline. |
| `unsupported_version` | The client and the host share no Host API version; the typed detail names the host's supported versions. |

## Hidden on purpose

| Code | Reason |
| --- | --- |
| `internal` | An unexpected failure; its detail stays in the host's logs and clients see only a generic message. |
