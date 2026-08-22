# Questions for a person

Anything an iteration could not decide without guessing. One bullet each:
the task id, the question, and what you did instead (built it per the spec,
skipped the task, chose a default).

Nobody is reading this while the loop runs. It is the handover.

## Open

- **A5** — the plan gives `Store.PreviewPathRules([]string) []string`, which can
  only apply the rules already stored; C3 asks the preview to prove a rule
  "before it is stored", and CLAUDE.md's `.env` import sets the rule that the
  dry run and the write are the same call. Built it as
  `PreviewPathRules(rules []model.PathRule, paths []string) ([]string, error)`
  — the same `preparePathRules` the save runs, so the dialog cannot describe
  something the press will not do. The error is how a pattern that will not
  compile is reported.
