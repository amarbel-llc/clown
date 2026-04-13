complete -c clown -w claude

function __clown_sessions
  # Encode PWD the way claude-code does: replace / with -
  set -l encoded (string replace -a '/' '-' (pwd))
  set -l project_dir ~/.claude/projects/$encoded

  # Build a map of session ID -> name from active session metadata
  set -l session_names
  for f in ~/.claude/sessions/*.json
    set -l sid (jq -r '.sessionId // empty' $f 2>/dev/null)
    set -l name (jq -r '.name // empty' $f 2>/dev/null)
    if test -n "$sid" -a -n "$name"
      set -a session_names "$sid=$name"
    end
  end

  # List all session transcripts for the current project
  if test -d "$project_dir"
    for f in $project_dir/*.jsonl
      set -l sid (string replace -r '.*/(.*)\.jsonl$' '$1' $f)
      test -n "$sid"; or continue

      # Look up name from active sessions
      set -l name ""
      for entry in $session_names
        if string match -q "$sid=*" $entry
          set name (string replace "$sid=" "" $entry)
          break
        end
      end

      if test -n "$name"
        echo -e "$name\t$sid"
      else
        echo -e "$sid"
      end
    end
  end
end

complete -c clown -l resume -f -r -a '(__clown_sessions)'
