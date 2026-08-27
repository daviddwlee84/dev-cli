# private key fixture

Contains a fake PEM-format private key block. `redact_private_keys`
must replace the whole block with `[REDACTED:private-key]` — the same
label SpecStory >= 2.4.0 emits for this class. A bare
mention of `PRIVATE KEY` in prose (below) is intentionally left as-is —
the redactor scopes to key *headers*, matching what detect-private-key
greps for, so prose no longer triggers a non-converging redact loop.

-----BEGIN RSA PRIVATE KEY----- <!-- gitleaks:allow -->
MIIEogIBAAKCAQEAFAKE_KEY_MATERIAL_FOR_TESTING_ONLY_DO_NOT_USE
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
-----END RSA PRIVATE KEY-----

And a bare mention of PRIVATE KEY outside any block — the redactor
must leave this untouched.
