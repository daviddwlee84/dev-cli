# SSH aliases disappear after a second Include

The static scanner used to let a child file's final Host/Match condition escape
into later sibling Includes, including sibling matches of the same glob.
For example, a first file ending in `Host prod-*` hid `dev-one` from a second file.
OpenSSH instead restores the caller's active state after each included file.

Keep lexical file order and parent Include constraints, but discard child-local
Host/Match state at the Include boundary. Regression coverage lives in
`TestDiscoverRestoresGuardStateAcrossGlobMatches` and
`TestDiscoverIncludeRestoresParentScope`. Organization tests compare isolated
before/after configurations through the real `ssh -G` command.

Never fix this by sorting Host declarations: first-obtained option precedence
still depends on their original order.
