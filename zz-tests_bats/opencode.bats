# Integration coverage for clown#197: verify a real opencode binary
# resolves the "custom/<slug>" model string clown's writeOpencodeConfigFile
# (cmd/clown/opencode.go) synthesizes when the OpenRouter model slug itself
# contains a "/" (e.g. "openai/gpt-4o" -> "custom/openai/gpt-4o"). The unit
# test (TestWriteOpencodeConfigFile_SlashModelSlug) only checks the
# generated JSON string; this drives the real opencode config loader.
#
# The config below is written by hand rather than exercised through
# `clown --profile ... opencode`, since clown writes its synthesized
# config to a self-cleaning temp dir with no interception point. The JSON
# shape must be kept in sync with writeOpencodeConfigFile's output.
#
# `opencode models <provider>` is pure config introspection — no network
# call — so a fake baseURL is safe here.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin OPENCODE_BIN opencode

  config_file="$BATS_TEST_TMPDIR/opencode.json"
  cat >"$config_file" <<'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "custom": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Custom Provider",
      "options": {"baseURL": "http://127.0.0.1:1/v1", "apiKey": "dummy"},
      "models": {
        "openai/gpt-4o": {"name": "openai/gpt-4o", "limit": {"context": 128000, "output": 16384}}
      }
    }
  },
  "model": "custom/openai/gpt-4o"
}
EOF
  export config_file
}

@test "opencode resolves a slash-containing OpenRouter model slug" {
  OPENCODE_CONFIG="$config_file" run "$OPENCODE_BIN" models custom
  assert_success
  assert_output --partial "custom/openai/gpt-4o"
}
