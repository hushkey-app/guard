#!/usr/bin/env bash
#
# The loop. Each iteration is a fresh process with a clean context window:
# it reads ralph/PROMPT.md, picks one task, does it, commits, and exits.
#
#   ralph/ralph.sh          # run until the plan is complete, or 100 iterations
#   ralph/ralph.sh 5        # run five iterations
#
# Stop it with Ctrl-C. Progress is on disk, so the next run resumes where this
# one stopped — that is the whole point of writing the plan to a file.

set -uo pipefail
cd "$(dirname "$0")/.."

MAX=${1:-100}
AGENT=${AGENT:-claude}
PROGRESS=ralph/memory/progress.md

if ! command -v "$AGENT" >/dev/null 2>&1; then
	echo "ralph: '$AGENT' not on PATH (override with AGENT=...)" >&2
	exit 1
fi

# The branch is created once, here, rather than by an iteration — a loop that
# might create a branch is a loop that might commit to the wrong one.
if ! git rev-parse --verify feat/analytics >/dev/null 2>&1; then
	echo "ralph: creating branch feat/analytics"
	git branch feat/analytics
fi
git switch feat/analytics || exit 1

for ((i = 1; i <= MAX; i++)); do
	if grep -q '^ALL TASKS COMPLETE$' "$PROGRESS" 2>/dev/null; then
		echo "ralph: plan complete after $((i - 1)) iterations"
		exit 0
	fi

	remaining=$(grep -c '^- \[ \]' ralph/IMPLEMENTATION_PLAN.md || true)
	echo
	echo "──────── iteration $i/$MAX · $remaining tasks unchecked · $(date '+%H:%M:%S') ────────"

	"$AGENT" -p --dangerously-skip-permissions < ralph/PROMPT.md
	status=$?

	if [ $status -ne 0 ]; then
		# A non-zero exit is usually a rate limit or a dropped connection, not a
		# reason to stop: the next iteration re-reads the plan and picks up from
		# whatever is on disk. Back off rather than hammer.
		echo "ralph: agent exited $status — backing off 60s"
		sleep 60
	fi

	# The working tree is the real check. An iteration that changed nothing
	# twice running is an iteration that is stuck, and looping on it burns
	# tokens producing nothing.
	head=$(git rev-parse HEAD)
	if [ "${head:-}" = "${last_head:-}" ]; then
		stuck=$((${stuck:-0} + 1))
		echo "ralph: no commit this iteration (${stuck} in a row)"
		if [ "$stuck" -ge 3 ]; then
			echo "ralph: three iterations with no commit — stopping. Read $PROGRESS and ralph/memory/questions.md."
			exit 1
		fi
	else
		stuck=0
	fi
	last_head=$head

	sleep 2
done

echo "ralph: reached the $MAX iteration limit with work outstanding"
