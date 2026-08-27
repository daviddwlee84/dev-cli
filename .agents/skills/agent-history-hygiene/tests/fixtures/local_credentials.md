# local credential shapes fixture

Credentials that leak from *local* workflows rather than SaaS dashboards:
an age encryption identity and a plain password/sudo-password assignment.
Both were promoted into the template after real transcripts hit them —
an age key copied between machines, and a root password pasted into chat
while walking through an on-box deploy.

The age key is synthetic filler but matches the rule's length and
character-class requirements (`AGE-SECRET-KEY-1` + 50–64 uppercase
alphanumerics). Avoid contiguous `ABCDEFGHIJKLMNOPQRSTUVWXYZ` — gitleaks'
default global allowlist treats the full alphabet as a stopword and would
suppress the match.

AGE_IDENTITY=AGE-SECRET-KEY-1QQQZZZWWWEEERRRTTTYYYUUUIIIOOOPPPQQQZZZWWWEEERRRTTT <!-- gitleaks:allow -->
password: hunter2seven <!-- gitleaks:allow -->
SUDO_PASSWORD=correcthorsebattery <!-- gitleaks:allow -->
passwd=swordfish99 <!-- gitleaks:allow -->
