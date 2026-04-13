# Tab completion for clown (https://github.com/amarbel-llc/clown)
# Based on fish-shell's claude.fish completions

# Top-level commands
complete -c clown -f -n __fish_use_subcommand -a doctor -d 'Check the health of your Claude Code auto-updater'
complete -c clown -f -n __fish_use_subcommand -a install -d 'Install Claude Code native build'
complete -c clown -f -n __fish_use_subcommand -a mcp -d 'Configure and manage MCP servers'
complete -c clown -f -n __fish_use_subcommand -a plugin -d 'Manage Claude Code plugins'
complete -c clown -f -n __fish_use_subcommand -a setup-token -d 'Set up a long-lived authentication token'
complete -c clown -f -n __fish_use_subcommand -a update -d 'Check for updates and install if available'

# Global options
complete -c clown -f -n __fish_use_subcommand -s h -l help -d 'Display help for command'
complete -c clown -f -n __fish_use_subcommand -s v -l version -d 'Output the version number'
complete -c clown -f -n __fish_use_subcommand -s p -l print -d 'Print response and exit (useful for pipes)'
complete -c clown -f -n __fish_use_subcommand -s c -l continue -d 'Continue the most recent conversation'
complete -c clown -x -s r -l resume -a '(clown-sessions 2>/dev/null)' -d 'Resume a conversation by session ID'
complete -c clown -f -n __fish_use_subcommand -l fork-session -d 'Create a new session ID when resuming'
complete -c clown -f -n __fish_use_subcommand -s d -l debug -d 'Enable debug mode'

# Model and agent options
complete -c clown -x -n __fish_use_subcommand -l model -d 'Model for the current session'
complete -c clown -x -n __fish_use_subcommand -l agent -d 'Agent for the current session'
complete -c clown -x -n __fish_use_subcommand -l agents -d 'JSON object defining custom agents'

# Prompt options
complete -c clown -x -n __fish_use_subcommand -l system-prompt -d 'System prompt to use for the session'
complete -c clown -x -n __fish_use_subcommand -l append-system-prompt -d 'Append a system prompt to the default'

# Tool and permission options
complete -c clown -x -n __fish_use_subcommand -l tools -d 'Specify the list of available tools'
complete -c clown -x -n __fish_use_subcommand -l allowed-tools -d 'Comma-separated list of tool names to allow'
complete -c clown -x -n __fish_use_subcommand -l disallowed-tools -d 'Comma-separated list of tool names to deny'
complete -c clown -f -n __fish_use_subcommand -l dangerously-skip-permissions -d 'Bypass all permission checks'
complete -c clown -f -n __fish_use_subcommand -l allow-dangerously-skip-permissions -d 'Enable bypassing permission checks as an option'
complete -c clown -x -n __fish_use_subcommand -l permission-mode -d 'Permission mode to use'

# Directory access options
complete -c clown -x -n __fish_use_subcommand -l add-dir -d 'Additional directories to allow tool access'

# MCP server options
complete -c clown -x -n __fish_use_subcommand -l mcp-config -d 'Load MCP servers from JSON files or strings'
complete -c clown -f -n __fish_use_subcommand -l strict-mcp-config -d 'Only use MCP servers from --mcp-config'

# Chrome integration options
complete -c clown -f -n __fish_use_subcommand -l chrome -d 'Enable Claude in Chrome integration'
complete -c clown -f -n __fish_use_subcommand -l no-chrome -d 'Disable Claude in Chrome integration'

# IDE options
complete -c clown -f -n __fish_use_subcommand -l ide -d 'Automatically connect to IDE on startup'

# Output and format options (for --print mode)
complete -c clown -x -n __fish_use_subcommand -l output-format -d 'Output format'
complete -c clown -x -n __fish_use_subcommand -l input-format -d 'Input format'
complete -c clown -f -n __fish_use_subcommand -l include-partial-messages -d 'Include partial message chunks'
complete -c clown -f -n __fish_use_subcommand -l replay-user-messages -d 'Re-emit user messages on stdout'

# Session and persistence options
complete -c clown -x -n __fish_use_subcommand -l session-id -d 'Use a specific session ID'
complete -c clown -f -n __fish_use_subcommand -l no-session-persistence -d 'Disable session persistence'

# Settings and configuration options
complete -c clown -x -n __fish_use_subcommand -l settings -d 'Load additional settings from file or JSON'
complete -c clown -x -n __fish_use_subcommand -l setting-sources -d 'Comma-separated list of setting sources'
complete -c clown -f -n __fish_use_subcommand -l disable-slash-commands -d 'Disable all skills'

# Plugin options
complete -c clown -x -n __fish_use_subcommand -l plugin-dir -d 'Load plugins from directories'

# API options
complete -c clown -x -n __fish_use_subcommand -l betas -d 'Beta headers to include in API requests'
complete -c clown -x -n __fish_use_subcommand -l max-budget-usd -d 'Maximum dollar amount to spend on API calls'
complete -c clown -x -n __fish_use_subcommand -l fallback-model -d 'Enable automatic fallback to specified model'

# JSON Schema options
complete -c clown -x -n __fish_use_subcommand -l json-schema -d 'JSON Schema for structured output validation'

# File resource options
complete -c clown -x -n __fish_use_subcommand -l file -d 'File resources to download at startup'

# Verbose option
complete -c clown -f -n __fish_use_subcommand -l verbose -d 'Override verbose mode setting'

# Install subcommand options
complete -c clown -f -n '__fish_seen_subcommand_from install' -a 'stable latest' -d 'Version to install'
complete -c clown -x -n '__fish_seen_subcommand_from install' -s h -l help -d 'Display help for install command'

# MCP subcommand
complete -c clown -f -n '__fish_seen_subcommand_from mcp' -s h -l help -d 'Display help for mcp command'

# Plugin subcommand
complete -c clown -f -n '__fish_seen_subcommand_from plugin' -s h -l help -d 'Display help for plugin command'

# Doctor subcommand
complete -c clown -f -n '__fish_seen_subcommand_from doctor' -s h -l help -d 'Display help for doctor command'

# Setup-token subcommand
complete -c clown -f -n '__fish_seen_subcommand_from setup-token' -s h -l help -d 'Display help for setup-token command'

# Update subcommand
complete -c clown -f -n '__fish_seen_subcommand_from update' -s h -l help -d 'Display help for update command'
