# Symbols source data

The SVG files under `files/` and `folders/`, and `mapping.json`, are vendored
from [miguelsolorio/vscode-symbols](https://github.com/miguelsolorio/vscode-symbols)
at commit `a405d32cca082d838ad7739b6d0044dea9dbbb79` (MIT; the license is at
`../../LICENSE-symbols.txt`).

Only the icons referenced by the reduced mapping are vendored. The mapping
keeps the upstream `fileNames`, `fileExtensions`, `folderNames` and
`rootFolderNames` entries whose icon is shipped. `skipped.json` lists the
candidate icons left out, with the reason.

Regenerate the Go data with:

	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateFileIconData
