package profilerules

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/cutting-garden/pkgs/ajour"
	"code.linenisgreat.com/cutting-garden/pkgs/trellis"
	"code.linenisgreat.com/piggy/go/pkgs/markl"
)

// A Finding is one rule a document breaks: the rule's id and severity, the
// node it is about (a path of ids, empty for a head or whole-document
// finding) and the message, its template filled.
type Finding struct {
	Rule     string
	Severity string
	Node     string
	Message  string
}

// Validate checks a capability profile: a hyphence document whose body is a
// stencil (RFC 0019 §1). A document that does not open with a `---`
// boundary is a head-less body, checked alone: the H-* and M-* rules do not
// run on it.
func Validate(document string) []Finding {
	head, body, hasHead := splitDocument(document)
	v := newValidator(LayerStencil)
	var mode string
	if hasHead {
		mode = v.checkHead(head)
	}
	trees := v.checkBody(body)
	if trees != nil && (mode == modeAttended || mode == modeUnattended) {
		v.checkMode(trees, mode)
	}
	return v.findings
}

// ValidatePatch checks an act's body as a patch: ajour.ParsePatch (every
// leaf denies) and the rules marked "patches too" (RFC 0019 §1).
func ValidatePatch(body string) []Finding {
	v := newValidator(LayerPatch)
	v.checkBody(body)
	return v.findings
}

const (
	modeAttended   = "attended"
	modeUnattended = "unattended"

	typeLine = "espalier-profile-v1"

	typePlugin       = "clown-plugin-v1"
	typeServer       = "mcp-server-v1"
	typeTool         = "mcp-tool-v1"
	typeToolInput    = "tool-input-v1"
	typeToolArg      = "tool-arg-v1"
	typeResources    = "mcp-resources-v1"
	typeResource     = "mcp-resource-v1"
	typeCgRoot       = "cg-root-v1"
	typeCgField      = "cg-field-v1"
	typeCgRootsRetd  = "cg-roots-v1"
	typeCgNodeRetd   = "cg-node-v1"
	layerCatalog     = "catalog"
	fieldOp          = "op"
	fieldPerm        = "perm"
	fieldKind        = "kind"
	fieldField       = "field"
	fieldValue       = "value"
	verbCreate       = "create"
	inputCommand     = "command"
	pluginMoxy       = "moxy"
	pluginHarness    = "clown-builtin-harness"
	serverHarness    = "harness"
	pluginJobs       = "clown-builtin-jobs"
	serverRingmaster = "ringmaster"
	serverTroupe     = "troupe"
	serverSpinclass  = "spinclass"
	toolChatSend     = "chat_send"
	tagRequired      = "required"
)

var (
	resourceVerbs = []string{"read", "patch", "put", "delete"}
	permValues    = []string{"allow", "ask", "dynamic"}
	headKeys      = []string{"role", "harness", "mode", "model", "provider", "spend_cap_usd", "_base"}
	requiredKeys  = []string{"role", "harness", "mode"}
	roleShape     = regexp.MustCompile(`^(manager|engineer)-[a-z0-9-]+$`)
	positiveDec   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	openCommand   = map[string]string{ // W-1's tools: plugin/server/tool → the input that is a command line
		pluginHarness + "/" + serverHarness + "/Bash": inputCommand,
		pluginMoxy + "/toolbox/run":                   inputCommand,
	}
	inertResourceTools = []string{"ReadMcpResourceTool", "ListMcpResourcesTool"}
)

type validator struct {
	layer    string
	findings []Finding
	// fields is RFC 0018 §1's type → fields; catalog, the catalog-layer types.
	fields      map[string][]string
	catalog     map[string]bool
	moxyServers []string
}

func newValidator(layer string) *validator {
	v := &validator{layer: layer, fields: map[string][]string{}, catalog: map[string]bool{}}
	contract, err := catalogcontract.Load()
	if err != nil {
		panic(fmt.Sprintf("profilerules: the embedded catalog contract does not parse: %v", err))
	}
	for _, nt := range contract.NodeTypes {
		v.fields[nt.Type] = nt.Fields
		v.catalog[nt.Type] = nt.Layer == layerCatalog
	}
	for _, t := range contract.Tools {
		if t.Plugin == pluginMoxy && !slices.Contains(v.moxyServers, t.Server) {
			v.moxyServers = append(v.moxyServers, t.Server)
		}
	}
	return v
}

// add records a finding for rule id at node; message is the rule's
// template, filled, without its `<id>: ` prefix.
func (v *validator) add(id, node, message string) {
	spec, ok := Rule(id)
	if !ok {
		panic("profilerules: no rule " + id)
	}
	v.findings = append(v.findings, Finding{Rule: id, Severity: spec.Severity, Node: node, Message: id + ": " + message})
}

func (v *validator) has(ids ...string) bool {
	return slices.ContainsFunc(v.findings, func(f Finding) bool { return slices.Contains(ids, f.Rule) })
}

// splitDocument splits a hyphence document into its metadata lines and its
// body (hyphence(7) DOCUMENT STRUCTURE, BODY SEPARATOR). A document that
// does not open with `---` has no head.
func splitDocument(document string) (head []string, body string, hasHead bool) {
	lines := strings.Split(document, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, document, false
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			rest := lines[i+1:]
			if len(rest) > 0 && rest[0] == "" {
				rest = rest[1:]
			}
			return lines[1:i], strings.Join(rest, "\n"), true
		}
	}
	return lines[1:], "", true
}

// checkHead applies H-1 to H-9 and returns the head's mode when it is
// single-valued, else "".
func (v *validator) checkHead(lines []string) string {
	values := map[string][]string{}
	var order []string
	typeSeen := ""
	for _, line := range lines {
		prefix, content, _ := strings.Cut(line, " ")
		switch prefix {
		case "!":
			// The type, without a trailing `@digest` lock.
			if words := strings.Fields(content); len(words) > 0 {
				typeSeen = words[0]
			}
		case "-", "<":
			key, value, isField := strings.Cut(content, "=")
			key = strings.TrimSpace(key)
			if !isField {
				key, value = strings.TrimSpace(content), ""
			}
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = append(values[key], strings.TrimSpace(value))
		}
	}

	if typeSeen != typeLine {
		shown := typeSeen
		if shown == "" {
			shown = "missing"
		}
		v.add("H-1", "", fmt.Sprintf("the type line is %s; a capability profile is ! %s", shown, typeLine))
	}
	for _, key := range requiredKeys {
		switch n := len(values[key]); {
		case n == 0:
			v.add("H-2", "", key+" is required")
		case n > 1:
			v.add("H-2", "", key+" appears twice")
		}
	}
	for _, key := range order {
		if !slices.Contains(headKeys, key) {
			v.add("H-9", "", "unknown head key "+key)
		}
	}
	one := func(key string) (string, bool) {
		if len(values[key]) != 1 {
			return "", false
		}
		return values[key][0], true
	}

	if role, ok := one("role"); ok && !roleShape.MatchString(role) {
		v.add("H-8", "", fmt.Sprintf("role=%s: manager-<leaf> or engineer-<leaf>", role))
	}
	mode, ok := one("mode")
	if !ok {
		return ""
	}
	if mode != modeAttended && mode != modeUnattended {
		v.add("H-3", "", fmt.Sprintf("mode=%s: attended or unattended", mode))
		return ""
	}
	want := map[string]string{modeAttended: "claude-code", modeUnattended: "juggler-run"}[mode]
	if harness, ok := one("harness"); ok && harness != want {
		v.add("H-4", "", fmt.Sprintf("harness=%s with mode=%s: %s profiles run %s", harness, mode, mode, want))
	}
	for _, key := range []string{"provider", "spend_cap_usd"} {
		_, present := values[key]
		switch {
		case mode == modeAttended && present:
			v.add("H-5", "", fmt.Sprintf("%s is forbidden in %s profiles", key, mode))
		case mode == modeUnattended && !present:
			v.add("H-5", "", fmt.Sprintf("%s is required in %s profiles", key, mode))
		}
	}
	if capUSD, ok := one("spend_cap_usd"); ok && mode == modeUnattended {
		if f, err := strconv.ParseFloat(capUSD, 64); !positiveDec.MatchString(capUSD) || err != nil || f <= 0 {
			v.add("H-5", "", fmt.Sprintf("spend_cap_usd=%s is not a positive decimal", capUSD))
		}
	}
	if _, present := values["model"]; mode == modeUnattended && !present {
		v.add("H-6", "", "model is required in unattended profiles (it is what the spend cap buys)")
	}
	base, present := values["_base"]
	switch {
	case !present && mode == modeUnattended:
		v.add("H-7", "", "_base is required in unattended profiles")
	case present && len(base) > 1:
		v.add("H-7", "", "_base appears twice")
	case present:
		digest, isLock := strings.CutPrefix(base[0], "@")
		if !isLock {
			v.add("H-7", "", fmt.Sprintf("_base = %s is not one @<markl id>", base[0]))
		} else if reason := invalidMarklID(digest); reason != "" {
			v.add("H-7", "", fmt.Sprintf("_base = %s is not a valid markl id (%s)", base[0], reason))
		}
	}
	return mode
}

// invalidMarklID returns why digest is not a blake2b256 markl id (R-11), or
// "" when it is one.
func invalidMarklID(digest string) string {
	if !strings.HasPrefix(digest, "blake2b256-") {
		return "its format is not blake2b256"
	}
	var id markl.Id
	if err := id.Set(digest); err != nil {
		return err.Error()
	}
	return ""
}

// checkBody applies the body rules (R-*, W-*) and returns the parsed trees,
// nil when the body does not parse as a literal tree (R-1).
func (v *validator) checkBody(body string) []trellis.TreeNode {
	trees, err := trellis.ParseTree(body)
	if err != nil {
		v.add("R-1", "", err.Error())
		return nil
	}
	for _, tree := range trees {
		v.walk(tree, nil)
	}
	// ajour's parser refuses R-4's and R-14's forms itself; those are named
	// above. Any other refusal of its (a qualifier value, a `_` field, a regex
	// that does not compile, a patch leaf without deny) is R-1.
	if !v.has("R-4", "R-14") {
		var perr error
		if v.layer == LayerPatch {
			_, perr = ajour.ParsePatch(body)
		} else {
			_, perr = ajour.ParseStencil(body)
		}
		if perr != nil {
			v.add("R-1", "", perr.Error())
		}
	}
	return trees
}

// walk applies the per-node rules to n, whose ancestors are path (root
// first), then recurses.
func (v *validator) walk(n trellis.TreeNode, path []trellis.TreeNode) {
	at := spellPath(append(path, n))
	stencil := v.layer == LayerStencil
	deny := slices.Contains(n.Tags, ajour.TagDeny)

	if n.Kind == trellis.IDNull && n.Type == "" {
		v.add("R-2", at, "[_ …] has no !type: the null id matches by type and atoms, so say which type (e.g. [-> _ !tool-input-v1 open])")
	}
	if n.Kind == trellis.IDTemp && n.Type == "" {
		v.add("R-3", at, "[+ …] has no !type: a creation allowance names the type it creates")
	}
	if stencil && n.Kind == trellis.IDNull && n.Type != "" && !hasCondition(n) && !slices.Contains(n.Tags, ajour.TagOpen) {
		v.add("R-4", at, fmt.Sprintf("[_ !%s] has no atoms and no open tag: write [_ !%s open] to admit every %s, or add atoms", n.Type, n.Type, n.Type))
	}
	if stencil && n.Kind == trellis.IDNull && (n.Type == typePlugin || n.Type == typeServer) && !everyLeafDenies(n) {
		level := map[string]string{typePlugin: "plugin", typeServer: "server"}[n.Type]
		v.add("R-5", at, fmt.Sprintf("[_ !%s] admits through every %s: name it, or end every branch beneath it in deny", n.Type, level))
	}
	v.checkAtoms(n, at, deny)
	switch n.Type {
	case typeToolArg:
		v.add("R-9", at, "tool-arg-v1 is retired: write [-> _ !tool-input-v1 field=<arg> value=[…]] beside [-> _ !tool-input-v1 ^field=<arg> open]")
	case typeCgRootsRetd, typeCgNodeRetd:
		v.add("R-10", at, n.Type+" is retired: hang cg roots under [-> resources !mcp-resources-v1] and give each step the plugin's own type (or none)")
	}
	if n.Lock != "" {
		if reason := invalidMarklID(n.Lock); reason != "" {
			v.add("R-11", at, fmt.Sprintf("%s@%s is not a valid markl id (%s): copy the lock from the catalog listing", n.ID, n.Lock, reason))
		}
	}
	if stencil && v.isResourceStep(n, path) && n.Kind != trellis.IDTemp && n.Type != typeCgField &&
		!deny && (len(n.Children) == 0 || slices.Contains(n.Tags, ajour.TagSubtree) || n.Globstar) && opAtom(n) == nil {
		v.add("R-12", at, at+" admits every verb: say which, e.g. op=[read]")
	}
	if stencil && n.Type == typeCgField && len(path) > 0 {
		parent := path[len(path)-1]
		if op := opAtom(parent); op == nil || !slices.ContainsFunc(op.Pred.Values, func(val trellis.Value) bool {
			text := trellis.ValueText(val)
			return text == "patch" || text == "put"
		}) {
			parentAt := spellPath(path)
			v.add("R-13", at, fmt.Sprintf("cg-field-v1 under %s is unreachable unless %s admits op=patch or op=put", parentAt, parentAt))
		}
	}
	if stencil && n.Type == typeTool {
		v.checkTool(n, path, at)
	}
	if v.catalog[n.Type] && (slices.Contains(n.Tags, ajour.TagSubtree) || n.Globstar) {
		v.add("W-4", at, fmt.Sprintf("subtree on %s is inert: catalog ids are not paths", n.Type))
	}

	for _, c := range n.Children {
		v.walk(c, append(slices.Clone(path), n))
	}
}

// checkAtoms applies the atom rules: R-6, R-7, R-8, R-14.
func (v *validator) checkAtoms(n trellis.TreeNode, at string, deny bool) {
	fields, known := v.fields[n.Type]
	perms := 0
	for _, atom := range n.Atoms {
		name := atom.Pred.Field.Name
		if atom.Pred.Field.Quoted {
			continue
		}
		spelled := trellis.SpellTreeAtom(atom)
		if name == fieldPerm {
			perms++
			why := permRefusal(atom, perms, deny, v.layer == LayerPatch)
			// Extra perm atoms are one finding for the node, not one each.
			if why != "" && !(why == whyOnePerm && perms > 2) {
				v.add("R-14", at, fmt.Sprintf("atom `%s`: %s", spelled, why))
			}
			continue
		}
		if name == fieldOp {
			for _, val := range atom.Pred.Values {
				text := trellis.ValueText(val)
				switch {
				case text == verbCreate:
					v.add("R-6", at, fmt.Sprintf("op=create at %s: creation is spelled by a + node ([-> + !<type> …]), never by op=create", at))
				case !slices.Contains(resourceVerbs, text):
					v.add("R-8", at, fmt.Sprintf("op=%s at %s: verbs are read, patch, put, delete", text, at))
				}
			}
		}
		if known && !slices.Contains(fields, name) {
			v.add("R-7", at, fmt.Sprintf("%s is not a field of %s (it carries: %s); an atom on a missing field never holds", name, n.Type, strings.Join(fields, ", ")))
		}
	}
}

const whyOnePerm = "a node carries one `perm` atom"

// permRefusal returns ajour's reason for refusing a perm atom (R-14), "" when
// the atom is well formed; nth counts the node's perm atoms so far.
func permRefusal(atom trellis.TreeAtom, nth int, deny, patch bool) string {
	switch {
	case nth > 1:
		return whyOnePerm
	case atom.Negate:
		return "`perm` is a decision, not a condition, so it cannot be negated"
	case atom.Pred.Op != trellis.FieldOpEq:
		return "`perm` takes `=` only"
	case len(atom.Pred.Values) != 1:
		return "`perm` takes one value"
	}
	value := trellis.ValueText(atom.Pred.Values[0])
	switch {
	case patch && value != permAskValue:
		// A patch only tightens (ajour 9e7b1be).
		return "a patch may only deny or ask; `perm=allow` and `perm=dynamic` loosen, so they belong in a stencil"
	case !slices.Contains(permValues, value):
		return "`perm` is allow, ask or dynamic"
	case deny:
		return "a node that carries `deny` decides nothing else"
	}
	return ""
}

const permAskValue = "ask"

// checkTool applies the tool-node warnings: W-1, W-2, W-5, W-6.
func (v *validator) checkTool(n trellis.TreeNode, path []trellis.TreeNode, at string) {
	plugin, server := "", ""
	if len(path) >= 2 {
		plugin, server = path[len(path)-2].ID, path[len(path)-1].ID
	}
	if input, ok := openCommand[plugin+"/"+server+"/"+n.ID]; ok && !slices.Contains(n.Tags, ajour.TagDeny) && admitsOpenInput(n, input) {
		v.add("W-1", at, fmt.Sprintf("%s admitted with an open command: the command line names files and hosts ajour cannot scope (RFC 0018 §7)", at))
	}
	if plugin == pluginHarness && server == serverHarness && slices.Contains(inertResourceTools, n.ID) {
		v.add("W-2", at, n.ID+" presents no tool step; this node is inert. Allow the resource under its owner's surface (RFC 0018 §7)")
	}
	if n.Kind == trellis.IDReal {
		if strings.HasPrefix(n.ID, "mcp__") {
			v.add("W-5", at, fmt.Sprintf("%s is Claude Code's spelling of a harness tool name; it matches nothing: write the server and tool as their own steps (RFC 0018 §2)", n.ID))
		} else if plugin == pluginMoxy {
			for _, s := range v.moxyServers {
				if t, ok := strings.CutPrefix(n.ID, s+"_"); ok && t != "" {
					v.add("W-5", at, fmt.Sprintf("%s is Claude Code's spelling of moxy's %s.%s; it matches nothing: write [-> %s !mcp-server-v1 [-> %s !mcp-tool-v1 …]]", n.ID, s, t, s, t))
					break
				}
			}
		}
	}
	if plugin == pluginMoxy {
		for _, atom := range n.Atoms {
			if atom.Pred.Field.Name == fieldKind && !atom.Pred.Field.Quoted {
				vals := make([]string, len(atom.Pred.Values))
				for i, val := range atom.Pred.Values {
					vals[i] = trellis.ValueText(val)
				}
				v.add("W-6", at, fmt.Sprintf("kind=%s at %s never holds for a moxy tool (none presents a kind, clown#356): name the tool, e.g. [-> read !mcp-tool-v1 …]", strings.Join(vals, ","), at))
			}
		}
	}
}

// admitsOpenInput reports whether tool node n admits its input named input
// with no value condition: an input node that reaches it (by its id, or a `_`
// node whose field atoms do not exclude it) and carries no `value` atom, or
// no input node at all.
func admitsOpenInput(n trellis.TreeNode, input string) bool {
	inputs := 0
	for _, c := range n.Children {
		if c.Type != typeToolInput {
			continue
		}
		inputs++
		if slices.Contains(c.Tags, ajour.TagDeny) || !inputNodeReaches(c, input) {
			continue
		}
		if !slices.ContainsFunc(c.Atoms, func(a trellis.TreeAtom) bool { return a.Pred.Field.Name == fieldValue }) {
			return true
		}
	}
	return inputs == 0
}

func inputNodeReaches(c trellis.TreeNode, input string) bool {
	if c.Kind == trellis.IDReal {
		return c.ID == input
	}
	for _, a := range c.Atoms {
		if a.Pred.Field.Name != fieldField {
			continue
		}
		names := make([]string, len(a.Pred.Values))
		for i, val := range a.Pred.Values {
			names[i] = trellis.ValueText(val)
		}
		if a.Negate == slices.Contains(names, input) {
			return false
		}
	}
	return true
}

// isResourceStep reports whether n is a resource-step node (R-12): typed
// mcp-resources-v1, mcp-resource-v1 or cg-root-v1, or a typed node beneath
// an mcp-resources-v1 step whose type is not RFC 0018's (a plugin's cg type).
func (v *validator) isResourceStep(n trellis.TreeNode, path []trellis.TreeNode) bool {
	switch n.Type {
	case typeResources, typeResource, typeCgRoot:
		return true
	case "":
		return false
	}
	if _, rfc0018 := v.fields[n.Type]; rfc0018 {
		return false
	}
	return slices.ContainsFunc(path, func(p trellis.TreeNode) bool { return p.Type == typeResources })
}

// checkMode applies the mode rules (M-2, M-3) under the head's mode.
func (v *validator) checkMode(trees []trellis.TreeNode, mode string) {
	switch mode {
	case modeUnattended:
		for _, plugin := range trees {
			for _, server := range plugin.Children {
				at := spellPath([]trellis.TreeNode{plugin, server})
				if server.Type == typeServer && server.ID == serverSpinclass && !slices.Contains(server.Tags, ajour.TagDeny) {
					v.add("M-2", at, at+" in an unattended profile: a node reports through its job")
				}
				if server.ID != serverTroupe {
					continue
				}
				for _, tool := range server.Children {
					if tool.ID == toolChatSend && !slices.Contains(tool.Tags, ajour.TagDeny) {
						at := spellPath([]trellis.TreeNode{plugin, server, tool})
						v.add("M-2", at, at+" in an unattended profile: a node reports through its job")
					}
				}
			}
		}
	case modeAttended:
		for _, want := range []string{serverRingmaster, serverTroupe} {
			found := slices.ContainsFunc(trees, func(plugin trellis.TreeNode) bool {
				return plugin.ID == pluginJobs && slices.ContainsFunc(plugin.Children, func(server trellis.TreeNode) bool {
					return server.ID == want && slices.Contains(server.Tags, tagRequired)
				})
			})
			if !found {
				v.add("M-3", "", fmt.Sprintf("attended profiles require %s/%s tagged required (the hello)", pluginJobs, want))
			}
		}
	}
}

// hasCondition reports whether n carries an atom that matches something: any
// atom but perm (R-4).
func hasCondition(n trellis.TreeNode) bool {
	return slices.ContainsFunc(n.Atoms, func(a trellis.TreeAtom) bool {
		return a.Pred.Field.Quoted || a.Pred.Field.Name != fieldPerm
	})
}

func everyLeafDenies(n trellis.TreeNode) bool {
	if len(n.Children) == 0 {
		return slices.Contains(n.Tags, ajour.TagDeny)
	}
	for _, c := range n.Children {
		if !everyLeafDenies(c) {
			return false
		}
	}
	return true
}

func opAtom(n trellis.TreeNode) *trellis.TreeAtom {
	for i, a := range n.Atoms {
		if a.Pred.Field.Name == fieldOp && !a.Pred.Field.Quoted && !a.Negate {
			return &n.Atoms[i]
		}
	}
	return nil
}

// spellPath spells a node by the ids from its root: moxy/folio/read, with
// `_` for the null id and `+` (or `+id`) for a temp id.
func spellPath(path []trellis.TreeNode) string {
	parts := make([]string, len(path))
	for i, p := range path {
		switch p.Kind {
		case trellis.IDNull:
			parts[i] = "_"
		case trellis.IDTemp:
			parts[i] = "+" + p.ID
		default:
			parts[i] = p.ID
		}
	}
	return strings.Join(parts, "/")
}
