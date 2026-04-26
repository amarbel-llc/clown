# Tab completion for clown (https://github.com/amarbel-llc/clown)
# Provider-aware: detects --provider flag or CLOWN_PROVIDER env var to
# offer Claude or Codex completions from a single command.

# --- Provider detection helpers ---

function __clown_provider
    set -l tokens (commandline -opc)
    for i in (seq 2 (count $tokens))
        if test "$tokens[$i]" = --provider; and test (math $i + 1) -le (count $tokens)
            echo $tokens[(math $i + 1)]
            return
        else if string match -q -- '--provider=*' $tokens[$i]
            string replace -- '--provider=' '' $tokens[$i]
            return
        end
    end
    if set -q CLOWN_PROVIDER
        echo $CLOWN_PROVIDER
    else
        echo claude
    end
end

function __clown_is_claude
    test (__clown_provider) = claude
end

function __clown_is_codex
    test (__clown_provider) = codex
end

# --- Provider selection (always available) ---
complete -c clown -x -n __fish_use_subcommand -l provider -a 'claude codex' -d 'Coding agent provider'

# --- Common options ---
complete -c clown -f -n __fish_use_subcommand -l naked -d 'Run provider without clown modifications'
complete -c clown -f -n __fish_use_subcommand -s h -l help -d 'Display help'
complete -c clown -f -n __fish_use_subcommand -l version -d 'Output the version number'
complete -c clown -x -n __fish_use_subcommand -l model -d 'Model for the current session'
complete -c clown -x -n __fish_use_subcommand -l add-dir -d 'Additional directories to allow tool access'

# ============================================================
# Claude-specific completions
# ============================================================

# Claude subcommands
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a doctor -d 'Check the health of your Claude Code auto-updater'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a install -d 'Install Claude Code native build'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a mcp -d 'Configure and manage MCP servers'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a plugin -d 'Manage Claude Code plugins'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a setup-token -d 'Set up a long-lived authentication token'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -a update -d 'Check for updates and install if available'

# Claude global options
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -s v -d 'Output the version number'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -s p -l print -d 'Print response and exit (useful for pipes)'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -s c -l continue -d 'Continue the most recent conversation'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -s r -l resume -d 'Resume a conversation by session ID'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l fork-session -d 'Create a new session ID when resuming'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -s d -l debug -d 'Enable debug mode'

# Claude model and agent options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l agent -d 'Agent for the current session'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l agents -d 'JSON object defining custom agents'

# Claude prompt options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l system-prompt -d 'System prompt to use for the session'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l append-system-prompt -d 'Append a system prompt to the default'

# Claude tool and permission options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l tools -d 'Specify the list of available tools'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l allowed-tools -d 'Comma-separated list of tool names to allow'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l disallowed-tools -d 'Comma-separated list of tool names to deny'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l dangerously-skip-permissions -d 'Bypass all permission checks'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l allow-dangerously-skip-permissions -d 'Enable bypassing permission checks as an option'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l permission-mode -d 'Permission mode to use'

# Claude MCP server options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l mcp-config -d 'Load MCP servers from JSON files or strings'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l strict-mcp-config -d 'Only use MCP servers from --mcp-config'

# Claude Chrome integration options
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l chrome -d 'Enable Claude in Chrome integration'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l no-chrome -d 'Disable Claude in Chrome integration'

# Claude IDE options
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l ide -d 'Automatically connect to IDE on startup'

# Claude output and format options (for --print mode)
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l output-format -d 'Output format'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l input-format -d 'Input format'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l include-partial-messages -d 'Include partial message chunks'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l replay-user-messages -d 'Re-emit user messages on stdout'

# Claude session and persistence options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l session-id -d 'Use a specific session ID'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l no-session-persistence -d 'Disable session persistence'

# Claude settings and configuration options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l settings -d 'Load additional settings from file or JSON'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l setting-sources -d 'Comma-separated list of setting sources'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l disable-slash-commands -d 'Disable all skills'

# Claude plugin options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l plugin-dir -d 'Load plugins from directories'

# Claude API options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l betas -d 'Beta headers to include in API requests'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l max-budget-usd -d 'Maximum dollar amount to spend on API calls'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l fallback-model -d 'Enable automatic fallback to specified model'

# Claude JSON Schema options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l json-schema -d 'JSON Schema for structured output validation'

# Claude file resource options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_claude' -l file -d 'File resources to download at startup'

# Claude verbose option
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_claude' -l verbose -d 'Override verbose mode setting'

# Claude subcommand options
complete -c clown -f -n '__fish_seen_subcommand_from install; and __clown_is_claude' -a 'stable latest' -d 'Version to install'
complete -c clown -f -n '__fish_seen_subcommand_from install; and __clown_is_claude' -s h -l help -d 'Display help for install command'
complete -c clown -f -n '__fish_seen_subcommand_from mcp; and __clown_is_claude' -s h -l help -d 'Display help for mcp command'
complete -c clown -f -n '__fish_seen_subcommand_from plugin; and __clown_is_claude' -s h -l help -d 'Display help for plugin command'
complete -c clown -f -n '__fish_seen_subcommand_from doctor; and __clown_is_claude' -s h -l help -d 'Display help for doctor command'
complete -c clown -f -n '__fish_seen_subcommand_from setup-token; and __clown_is_claude' -s h -l help -d 'Display help for setup-token command'
complete -c clown -f -n '__fish_seen_subcommand_from update; and __clown_is_claude' -s h -l help -d 'Display help for update command'

# ============================================================
# Codex-specific completions
# ============================================================

# Codex subcommands
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a exec -d 'Run Codex non-interactively'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a review -d 'Run a code review non-interactively'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a login -d 'Manage login'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a logout -d 'Remove stored authentication credentials'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a mcp -d 'Manage external MCP servers for Codex'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a resume -d 'Resume a previous interactive session'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a fork -d 'Fork a previous interactive session'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a completion -d 'Generate shell completion scripts'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -a help -d 'Print help'

# Codex shared options
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s c -l config -d 'Override a configuration value'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -l enable -d 'Enable a feature'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -l disable -d 'Disable a feature'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -l remote -d 'Connect to a remote app server'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -l remote-auth-token-env -d 'Bearer token env var for remote mode'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s i -l image -d 'Attach image files'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -l oss -d 'Use the local OSS model provider'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -l local-provider -d 'Select the local provider'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s p -l profile -d 'Configuration profile'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s s -l sandbox -a 'read-only workspace-write danger-full-access' -d 'Sandbox mode'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s a -l ask-for-approval -a 'untrusted on-failure on-request never' -d 'Approval policy'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -l full-auto -d 'Run with workspace-write and on-request approvals'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -l dangerously-bypass-approvals-and-sandbox -d 'Disable sandbox and approvals'
complete -c clown -x -n '__fish_use_subcommand; and __clown_is_codex' -s C -l cd -d 'Working directory'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -l search -d 'Enable live web search'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -l no-alt-screen -d 'Disable alternate screen mode'
complete -c clown -f -n '__fish_use_subcommand; and __clown_is_codex' -s V -d 'Display version'
# Codex resume and fork subcommand flag completions. Value lists for
# the session id were removed when bin/clown-sessions was deleted; see
# issue #27 for restoring codex thread-id completion via a Go-native
# enumerator.
complete -c clown -f -n '__fish_seen_subcommand_from resume; and __clown_is_codex' -l last -d 'Resume the most recent session'
complete -c clown -f -n '__fish_seen_subcommand_from resume; and __clown_is_codex' -l all -d 'Show all sessions'
complete -c clown -f -n '__fish_seen_subcommand_from resume; and __clown_is_codex' -l include-non-interactive -d 'Include non-interactive sessions'
complete -c clown -f -n '__fish_seen_subcommand_from fork; and __clown_is_codex' -l last -d 'Fork the most recent session'
complete -c clown -f -n '__fish_seen_subcommand_from fork; and __clown_is_codex' -l all -d 'Show all sessions'
complete -c clown -f -n '__fish_seen_subcommand_from fork; and __clown_is_codex' -l include-non-interactive -d 'Include non-interactive sessions'

# ============================================================
# Top-level clown subcommands (provider-agnostic)
# ============================================================

# `resume` and `sessions-complete` are clown's own subcommands, offered
# regardless of which provider is selected.
complete -c clown -f -n __fish_use_subcommand -a resume -d 'Resume a session in $PWD (claude only)'
complete -c clown -f -n __fish_use_subcommand -a sessions-complete -d 'Emit fish completion lines for sessions'

# Predicate for top-level `clown resume <args>` — token 2 is exactly
# `resume`. Distinct from codex's native `resume` subcommand, which is
# reached via `clown -- resume` and thus has `--` between tokens.
function __clown_at_resume
    set -l tokens (commandline -opc)
    test (count $tokens) -ge 2; and test "$tokens[2]" = "resume"
end

complete -c clown -x -n __clown_at_resume -a '(clown sessions-complete --pwd-only 2>/dev/null)' -d 'Resume by URI'
complete -c clown -x -n __clown_at_resume -l provider -a claude -d 'Provider (claude only in v1)'
complete -c clown -f -n __clown_at_resume -s y -l yes -d 'Skip confirmation dialog'
complete -c clown -f -n __clown_at_resume -s h -l help -d 'Show help for clown resume'
