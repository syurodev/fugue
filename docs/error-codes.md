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
| `frame_too_large` | A stream frame exceeded the size limit. |
| `interaction.not_found` | The interaction request does not exist or is no longer available. |
| `interaction.already_answered` | The interaction request has already been answered. |
| `interaction.stale_incarnation` | The interaction request belongs to a previous host incarnation. |
| `interaction.option_not_offered` | The selected outcome or option was not offered. |
| `interaction.text_too_large` | The interaction answer text or deny note exceeds the size limit. |
| `workspace.not_found` | The workspace does not exist. |
| `workspace.already_exists` | The workspace already exists. |
| `workspace.config_invalid` | The workspace configuration is invalid; safe key and source position may be included. |
| `workspace.config_changed` | The workspace configuration changed since it was reviewed. |
| `workspace.config_unconfirmed` | The workspace configuration requires confirmation. |
| `workspace.root_invalid` | The workspace root is invalid. |
| `workspace.root_missing` | The workspace root is missing. |
| `workspace.path_denied` | The workspace path is not allowed. |
| `workspace.ownership_invalid` | Workspace ownership or permissions are invalid. |
| `workspace.config_too_large` | The workspace configuration exceeds the size limit. |
| `vendor.unsupported` | The vendor or operation is not supported in this phase. |
| `vendor.session_not_found` | The vendor session does not exist or is not live. |
| `vendor.session_state` | The vendor session state does not allow the operation (busy, closed or not resumable). |
| `vendor.turn_not_active` | No vendor turn is running. |
| `vendor.capacity` | The host already runs the maximum number of vendor sessions. |
| `vendor.vault_locked` | The vault is locked; vendor sessions cannot start or take input. |
| `vendor.invalid_profile` | A session profile value is not allowed. |
| `vendor.unknown_command` | The /fugue: command is not available; it never reaches the vendor. |
| `vendor.command_conflict` | The command_id was already used for a different command. |
| `vendor.workspace_mismatch` | The session is bound to the workspace root it was created in; resume cannot move it. |

## Hidden on purpose

| Code | Reason |
| --- | --- |
| `internal` | An unexpected failure; its detail stays in the host's logs and clients see only a generic message. |
