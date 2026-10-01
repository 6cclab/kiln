package settings

import "testing"

// TestIsReadOnlyCommand pins the plan-mode bash allowlist
// (qa/findings *plan-mode-denies-read-only-bash). Every "no" row is a way a
// command could write or run something while looking like a read.
func TestIsReadOnlyCommand(t *testing.T) {
	yes := []string{
		`cat SPEC.md`,
		`cat SPEC.md 2>/dev/null && echo "---LS---" && ls -la`,
		`rtk read SPEC.md 2>/dev/null && echo "---LS---" && rtk ls -la`,
		`ls -la | head -20`,
		`grep -rn 'func main' . | wc -l`,
		`find . -name '*.go' -not -path './vendor/*'`,
		`git status --short; git log --oneline -5`,
		`git diff HEAD~1 -- api/`,
		`git branch --show-current`,
		`go version && go env GOPATH`,
		`node --version || true`,
		`sed -n '1,40p' main.go`,
		`wc -l < main.go`,
		`cd api && ls`,
		`tree -L 2`,
		`rtk git log -3`,
		`[ -f go.mod ] && cat go.mod`,
		`ls >/dev/null 2>&1`,
	}
	no := []string{
		``,
		`rm -rf build`,
		`echo hi > out.txt`,
		`cat a >> b`,
		`cat a 1>out`,
		`ls && touch x`,
		`cat $(which go)`,
		"cat `ls`",
		`echo "$HOME"`,
		`cat ${HOME}/x`,
		`ls & rm x`,
		`find . -name '*.tmp' -delete`,
		`find . -exec rm {} \;`,
		`sed -i 's/a/b/' f`,
		`sed -n '1p;w out' f`,
		`sed 's/a/b/' f`,
		`sort -o f f`,
		`uniq in out`,
		`git commit -m x`,
		`git branch -D main`,
		`git checkout main`,
		`git -c core.pager=sh log`,
		`git diff --output=patch`,
		`git config user.name x`,
		`go build ./...`,
		`go env -w GOFLAGS=x`,
		`npm install`,
		`env rm x`,
		`FOO=1 ls`,
		`./script.sh`,
		`awk '{print}' f`,
		`xargs rm < list`,
		`cat <(rm x)`,
		`cat <<EOF`,
		"ls\nrm x",
		`echo 'unterminated`,
		`ls |`,
		`&& ls`,
		`rtk curl http://x`,
		`rtk proxy rm x`,
		`tee out < in`,
		`python3 -c 'print(1)'`,
		`rg --pre=sh x`,
		`date -s 2020-01-01`,
		`tree -o out`,
	}
	for _, c := range yes {
		if !IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = true, want false", c)
		}
	}
}

// TestDecidePlanModeBash: plan mode allows a read-only bash command and
// asks about anything else (Claude Code without its plan classifier).
func TestDecidePlanModeBash(t *testing.T) {
	if got := Decide(Permissions{}, "bash", "cat SPEC.md && ls -la", ModePlan); got != Allow {
		t.Errorf("read-only bash in plan mode = %v, want Allow", got)
	}
	if got := Decide(Permissions{}, "bash", "mkdir api", ModePlan); got != Ask {
		t.Errorf("mutating bash in plan mode = %v, want Ask", got)
	}
	if got := Decide(Permissions{Deny: []string{"Bash(cat *)"}}, "bash", "cat SPEC.md", ModePlan); got != Deny {
		t.Errorf("deny rule must still win in plan mode, got %v", got)
	}
}
