SYNTHETIC_PLUGIN_FRAGMENT_MARKER

This fragment is shipped by the synthetic-plugin under
`.clown-plugin/system-prompt-append.d/` to demonstrate the convention
defined in FDR 0003. clown's mkCircus picks up this file at build time
and routes it into the assembled append-mode system prompt between
clown's own builtin fragments and the user's `.circus/system-prompt.d/`
fragments.
