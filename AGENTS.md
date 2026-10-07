# codvps: agent and contributor guidelines

This repository enforces DCO (Developer Certificate of Origin) on all commits. Every commit must include a `Signed-off-by:` line:

```bash
git commit -s
```

For cloud agents and automated contributors:
- All commits must be DCO-signed (`git commit -s`)
- Push operations are explicit and human-initiated (`git push`)
- Never use `git push --no-verify` to bypass hooks
- Keep commit messages clear and descriptive

See `CONTRIBUTING.md` for development workflows and testing requirements.
