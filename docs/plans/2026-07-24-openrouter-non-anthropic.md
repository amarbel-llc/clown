# OpenRouter Non-Anthropic Models (Phase A) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add an `openrouter-opencode` profile template so users can run non-Anthropic models (GPT-4o, Gemini, Llama, etc.) via OpenRouter's OpenAI-compatible endpoint through opencode.

**Architecture:** Single new entry in `profileTemplates` (`cmd/clown/profileform.go`) with `provider=opencode, backend=gateway, URL=https://openrouter.ai/api/v1, Token=${OPENROUTER_API_KEY}, Model=openai/gpt-4o`. No schema changes. The existing opencode config synthesizer (`writeOpencodeConfigFile`) and gateway validation path already handle opencode+gateway correctly. The one implementation-time risk — whether `"custom/openai/gpt-4o"` is parsed correctly by opencode — is covered by a new unit test and a smoke-test recipe.

**Tech Stack:** Go, charmbracelet/huh (already vendored), just, bats.

**Rollback:** Purely additive — don't use the template. Any profiles already created via it can be removed with `clown profile remove <name>`.

---

### Task 1: Test that slash-containing OpenRouter model slugs survive the synthesized config

This guards the load-bearing assumption: `writeOpencodeConfigFile("openai/gpt-4o")` must
produce `"model":"custom/openai/gpt-4o"` and a map key of `"openai/gpt-4o"`, both present in
the raw JSON. If opencode splits only on the first `/`, `custom/openai/gpt-4o` resolves
correctly; if it splits further, the test still passes (it doesn't call opencode), but the
smoke recipe in Task 3 will catch the real-world failure.

**Files:**
- Modify: `cmd/clown/opencode_test.go`

**Step 1: Append the failing test**

Append to `opencode_test.go` (inside `package main`):

```go
func TestWriteOpencodeConfigFile_SlashModelSlug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	const slug = "openai/gpt-4o"
	if err := writeOpencodeConfigFile(path, "https://openrouter.ai/api/v1", "key", slug); err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// map key and "name" field must carry the full slug
	if !strings.Contains(content, `"openai/gpt-4o"`) {
		t.Errorf("slug missing from config: %s", content)
	}
	// model reference must be custom/<slug>
	if !strings.Contains(content, `"model":"custom/openai/gpt-4o"`) {
		t.Errorf("model ref wrong in config: %s", content)
	}
}
```

**Step 2: Run test to confirm it passes (no implementation change needed)**

```
go test ./cmd/clown/... -run TestWriteOpencodeConfigFile_SlashModelSlug -v
```

Expected: PASS — the existing synthesizer already produces the right output.
If it FAILS: the JSON marshaller is escaping `/` as `\/` (valid JSON but breaks
`strings.Contains`). Fix the assertion to use `strings.ReplaceAll(content, `\/`, `/`)` before
the Contains checks, or use `encoding/json.Compact` with `"/"` unescaped (add
`SetEscapeHTML(false)` to the encoder in `writeOpencodeConfigFile`). Document which it was
and commit the fix.

**Step 3: Commit**

```
git add cmd/clown/opencode_test.go
git commit -m "test: guard slash-model-slug in writeOpencodeConfigFile"
```

---

### Task 2: Add the `openrouter-opencode` profile template

**Files:**
- Modify: `cmd/clown/profileform.go`
- Modify: `cmd/clown/profileform_test.go`

**Step 1: Write the failing test first**

Append to `profileform_test.go`:

```go
func TestProfileTemplates_OpenRouterOpencode(t *testing.T) {
	p := templateByName(t, "openrouter-opencode")
	if p.Provider != "opencode" {
		t.Errorf("provider = %q, want opencode", p.Provider)
	}
	if p.Backend != "gateway" {
		t.Errorf("backend = %q, want gateway", p.Backend)
	}
	if p.URL != "https://openrouter.ai/api/v1" {
		t.Errorf("url = %q, want https://openrouter.ai/api/v1", p.URL)
	}
	if p.Token != "${OPENROUTER_API_KEY}" {
		t.Errorf("token = %q, want ${OPENROUTER_API_KEY}", p.Token)
	}
	if p.Model == "" {
		t.Error("model must be pre-filled (e.g. openai/gpt-4o)")
	}
	if err := profile.Validate(p); err != nil {
		t.Errorf("template profile must pass validation: %v", err)
	}
}
```

**Step 2: Run to confirm FAIL**

```
go test ./cmd/clown/... -run TestProfileTemplates_OpenRouterOpencode -v
```

Expected: FAIL with `no template "openrouter-opencode"`.

**Step 3: Add the template**

In `cmd/clown/profileform.go`, add after the existing `openrouter` entry in `profileTemplates`
(after line 83, before the `gateway` entry):

```go
	{
		key: "openrouter-opencode", display: "OpenRouter (opencode)",
		p: profile.Profile{
			Provider: "opencode", Backend: "gateway",
			URL:   "https://openrouter.ai/api/v1",
			Token: "${OPENROUTER_API_KEY}",
			Model: "openai/gpt-4o",
		},
		note: "Model is an OpenRouter slug (e.g. openai/gpt-4o, google/gemini-2.0-flash-001). Browse at https://openrouter.ai/models",
	},
```

**Step 4: Run tests to confirm PASS**

```
go test ./cmd/clown/... -run TestProfileTemplates -v
```

Expected: both `TestProfileTemplates` and `TestProfileTemplates_OpenRouterOpencode` PASS.

Also run the full package to catch regressions:

```
go test ./cmd/clown/... ./internal/profile/...
```

**Step 5: Commit**

```
git add cmd/clown/profileform.go cmd/clown/profileform_test.go
git commit -m "feat: add openrouter-opencode profile template (Phase A)

Adds an openrouter-opencode template with provider=opencode, backend=gateway,
URL=https://openrouter.ai/api/v1, Token=\${OPENROUTER_API_KEY}, Model=openai/gpt-4o.
Users run clown profile add, select 'OpenRouter (opencode)', edit the model slug,
then launch with clown --profile <name>.

Design: docs/plans/2026-07-24-openrouter-non-anthropic-design.md"
```

---

### Task 3: Add a smoke-test justfile recipe

The unit tests confirm the template exists and the config synthesizer handles slash slugs.
This recipe confirms the full path works against a live OpenRouter key.

**Files:**
- Modify: `justfile` (find the block containing `smoke-clown-against-openrouter` or
  `smoke-clown-against-tailnet` to place this nearby)

**Step 1: Read the existing smoke recipes to find the right placement**

```
grep -n "smoke-clown" justfile
```

**Step 2: Add the recipe**

Add after the existing `smoke-clown-against-openrouter` recipe:

```just
# Smoke-test opencode+openrouter with a slash model slug; needs OPENROUTER_API_KEY
[group('smoke')]
smoke-opencode-against-openrouter:
    clown profile add  # select 'OpenRouter (opencode)', accept defaults, save as opencode-openrouter
    clown --profile opencode-openrouter --version
```

Note: the recipe is intentionally interactive — it guides the user through a one-time
profile creation before launching. If a non-interactive smoke lane is needed later,
add a `clown-opencode-openrouter` builtin profile to `profiles/builtin.toml` with
`token = "${OPENROUTER_API_KEY}"` (mirrors the claude-openrouter pattern).

**Step 3: Commit**

```
git add justfile
git commit -m "just: add smoke-opencode-against-openrouter recipe"
```
