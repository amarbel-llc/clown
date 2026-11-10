package catalogserve

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// templateRegexp is cutting-garden RFC 0018 §3's Match for a Level 1
// template: each {name} captures a non-empty run with no "/".
func templateRegexp(tmpl string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(tmpl))
			break
		}
		j := strings.IndexByte(tmpl[i:], '}')
		b.WriteString(regexp.QuoteMeta(tmpl[:i]))
		b.WriteString("[^/]+")
		tmpl = tmpl[i+j+1:]
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// cutting-garden RFC 0018 §5 (its conformance point "uri template:
// container resolves to its body-declaring type"): every node type declares a
// template, and every URI the plugin emits matches its own type's template
// and no other's.
func TestEveryEmittedURIResolvesToItsOwnTypeOnly(t *testing.T) {
	s, _ := newServer(t)
	raw, rerr := call(t, s, "initialize", map[string]any{"protocol_versions": []string{ProtocolVersion}})
	if rerr != nil {
		t.Fatal(rerr)
	}
	var res struct {
		NodeTypes []struct {
			Tag         string `json:"tag"`
			URITemplate string `json:"uri_template"`
		} `json:"node_types"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	templates := map[string]*regexp.Regexp{}
	for _, nt := range res.NodeTypes {
		if nt.URITemplate == "" {
			t.Errorf("node type %s declares no uri_template", nt.Tag)
			continue
		}
		templates[nt.Tag] = templateRegexp(nt.URITemplate)
	}
	var walk func(uri string)
	walk = func(uri string) {
		for _, n := range list(t, s, uri) {
			for tag, re := range templates {
				if got, want := re.MatchString(n.URI), tag == n.Type; got != want {
					t.Errorf("%s (type %s) matches %s's template: %v", n.URI, n.Type, tag, got)
				}
			}
			if n.Type != typeInput {
				walk(n.URI)
			}
		}
	}
	walk("clown-catalog://live")
}
