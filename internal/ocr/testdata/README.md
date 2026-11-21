# OCR Conformance Fixtures

`ocr` (Alibaba Open Code Review) was not installed on the build machine when F11.5c was implemented.
Therefore, these JSON fixtures were written from the documented `alibaba/open-code-review` delegate-mode schema (`ocr delegate preview --format json` and `ocr delegate rule --format json`, `schema_version: "1"`):

- `preview.json`: valid `ocr delegate preview --format json` response with `schema_version: "1"`, `reviewable_files`, `excluded_files` (with reasons), and `bundles`.
- `rule.json`: valid `ocr delegate rule --format json` response with `schema_version: "1"` and rule `groups`.
- `preview_unknown_schema.json`: `preview` response with unsupported `schema_version: "99"` (must be treated as "no OCR").
- `rule_unknown_schema.json`: `rule` response with unsupported `schema_version: "99"` (must be treated as "no OCR").
