complete -c clown -w claude

function __clown_sessions
  for f in ~/.claude/sessions/*.json
    set -l sid (jq -r '.sessionId' $f 2>/dev/null)
    set -l name (jq -r '.name // empty' $f 2>/dev/null)
    set -l cwd (jq -r '.cwd // empty' $f 2>/dev/null)
    if test -n "$name"
      echo -e "$name\t$cwd ($sid)"
    else if test -n "$sid"
      echo -e "$sid\t$cwd"
    end
  end
end

complete -c clown -l resume -f -r -a '(__clown_sessions)'
