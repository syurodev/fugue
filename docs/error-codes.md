# Error codes

Every error code that can reach a client through the Host API is listed here, either as public
(clients may rely on it) or as hidden on purpose (clients see only a generic failure), with a
reason. `TestErrorCodesPublished` fails when `internal/platform/errcode` declares a `Code`
constant that is in neither list, in both, or when this page names a code that does not exist.

Changing this file needs a review of what clients can rely on; hiding a code needs a reason.

## Public

| Code | Meaning |
| --- | --- |

## Hidden on purpose

| Code | Reason |
| --- | --- |
