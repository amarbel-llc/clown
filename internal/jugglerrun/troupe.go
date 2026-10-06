package jugglerrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Credential is `troupe mint`'s output: the account JID and the path of the
// file holding its password (the credential by reference, never the secret).
type Credential struct {
	JID          string `json:"jid"`
	PasswordFile string `json:"password_file"`
}

// Localpart is the JID's localpart (TROUPE_XMPP_USER).
func (c Credential) Localpart() string {
	local, _, _ := strings.Cut(c.JID, "@")
	return local
}

// Domain is the JID's domain (TROUPE_XMPP_DOMAIN, the c2s vhost).
func (c Credential) Domain() string {
	_, domain, _ := strings.Cut(c.JID, "@")
	domain, _, _ = strings.Cut(domain, "/")
	return domain
}

// Identity is who a troupe call acts as. The zero Identity is the ambient one
// (the caller's own CLOWN_SESSION_ID and TROUPE_XMPP_* environment).
type Identity struct {
	SessionKey   string
	User         string
	PasswordFile string
	Domain       string
}

// IdentityFor is the identity of a minted principal.
func IdentityFor(sessionKey string, c Credential) Identity {
	return Identity{SessionKey: sessionKey, User: c.Localpart(), PasswordFile: c.PasswordFile, Domain: c.Domain()}
}

func (id Identity) isAmbient() bool { return id == Identity{} }

func (id Identity) environ() []string {
	if id.isAmbient() {
		return nil
	}
	return EnvWith(os.Environ(), map[string]string{
		SessionIDEnv:                id.SessionKey,
		"TROUPE_XMPP_USER":          id.User,
		"TROUPE_XMPP_PASSWORD_FILE": id.PasswordFile,
		"TROUPE_XMPP_DOMAIN":        id.Domain,
	})
}

// Wake is one standalone waking message to a principal's channel.
type Wake struct {
	Target    string
	From      string
	Source    string
	Message   string
	ResultRef string
}

// Troupe is the messaging surface juggler consumes.
type Troupe interface {
	// Mint creates the account for sessionKey and writes its password to
	// passwordFile: one file per identity, so a mint never overwrites the
	// ambient identity's credential (TROUPE_XMPP_PASSWORD_FILE).
	Mint(ctx context.Context, sessionKey, passwordFile string) (Credential, error)
	RevokeMint(ctx context.Context, sessionKey, passwordFile string) error
	// PostStanza posts one stanza into a MUC room as `as` and returns the
	// posted message id.
	PostStanza(ctx context.Context, as Identity, room, source string, stanza []byte) (string, error)
	SendWake(ctx context.Context, w Wake) error
}

// ExecTroupe shells the troupe binary (troupe(1)). Argv shapes:
//
//	mint --session-key K --password-file P   -> {"jid","password_file"}
//	mint-revoke --session-key K --password-file P   (file absent: exit 0, server
//	                                                 not contacted; login failure:
//	                                                 exit 1, see IsLoginFailure)
//	muc send --room R --subject <stanza JSON> --body "" --source S [--from K]
//	message --target T --source S --message M [--from F] [--result-ref R]
//
// The muc send shape (shared by run, spawn and decide) is clown-hook-tee's:
// the stanza JSON has no blank line, so all of it rides --subject with an
// empty --body and the wire body is exactly the stanza.
type ExecTroupe struct {
	Bin string
}

func (t ExecTroupe) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return runCommand(ctx, Command{Argv: append([]string{t.Bin}, args...), Env: env})
}

func (t ExecTroupe) Mint(ctx context.Context, sessionKey, passwordFile string) (Credential, error) {
	out, err := t.run(ctx, nil, "mint", "--session-key", sessionKey, "--password-file", passwordFile)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err := json.Unmarshal(bytes.TrimSpace(out), &c); err != nil {
		return Credential{}, fmt.Errorf("troupe mint: parsing output: %w", err)
	}
	if c.JID == "" || c.PasswordFile == "" {
		return Credential{}, fmt.Errorf("troupe mint: output lacks jid or password_file: %s", bytes.TrimSpace(out))
	}
	return c, nil
}

func (t ExecTroupe) RevokeMint(ctx context.Context, sessionKey, passwordFile string) error {
	_, err := t.run(ctx, nil, "mint-revoke", "--session-key", sessionKey, "--password-file", passwordFile)
	return err
}

// MUCSendArgv is the `troupe muc send` argv for one stanza.
func MUCSendArgv(bin, room, source, from string, stanza []byte) []string {
	argv := []string{bin, "muc", "send", "--room", room, "--subject", string(stanza), "--body", "", "--source", source}
	if from != "" {
		argv = append(argv, "--from", from)
	}
	return argv
}

func (t ExecTroupe) PostStanza(ctx context.Context, as Identity, room, source string, stanza []byte) (string, error) {
	argv := MUCSendArgv(t.Bin, room, source, as.SessionKey, stanza)
	out, err := t.run(ctx, as.environ(), argv[1:]...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (t ExecTroupe) SendWake(ctx context.Context, w Wake) error {
	args := []string{"message", "--target", w.Target, "--source", w.Source, "--message", w.Message}
	if w.From != "" {
		args = append(args, "--from", w.From)
	}
	if w.ResultRef != "" {
		args = append(args, "--result-ref", w.ResultRef)
	}
	_, err := t.run(ctx, nil, args...)
	return err
}

// loginFailureMarkers are the stderr fragments of a troupe verb that could
// not log in as the account it was handed. troupe (4c52b3b) cannot tell "no
// such account" from "wrong password", so at teardown both mean the account
// is already gone.
var loginFailureMarkers = []string{"xmpp: negotiate", "not-authorized"}

// IsLoginFailure reports whether err is a troupe exit 1 whose stderr says the
// login itself failed (see loginFailureMarkers), as opposed to the server
// being unreachable or the call being refused.
func IsLoginFailure(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) || ce.ExitCode != 1 {
		return false
	}
	for _, m := range loginFailureMarkers {
		if strings.Contains(ce.Stderr, m) {
			return true
		}
	}
	return false
}

// MUC affiliations `troupe muc affiliate` sets.
const (
	AffiliationOwner  = "owner"
	AffiliationMember = "member"
	AffiliationNone   = "none"
)

// RoomInfo is `troupe muc create`'s stdout object.
type RoomInfo struct {
	Room    string   `json:"room"`
	Created bool     `json:"created"`
	Owners  []string `json:"owners"`
	Members []string `json:"members"`
}

// RoomProvisioner provisions a run's MUC (FDR 0019 §7). Every call acts as
// `as`, which must own the room for anything but the create of a new one.
type RoomProvisioner interface {
	// CreateRoom creates room persistent, with `as` as its creator-owner and
	// owners as further owners; it is idempotent on a room `as` owns.
	CreateRoom(ctx context.Context, as Identity, room string, owners []string) (RoomInfo, error)
	// Affiliate sets the affiliation of every jid; none removes it. The last
	// owner cannot set its own affiliation to none.
	Affiliate(ctx context.Context, as Identity, room, affiliation string, jids ...string) error
}

// ExecRoomProvisioner shells troupe's MUC administration verbs (troupe ≥
// 4c52b3b). Argv shapes, run with `as`'s TROUPE_XMPP_* identity:
//
//	muc create --room R [--owner J]...            -> {"room","created","owners","members"}
//	muc affiliate --room R --affiliation A --jid J [--jid J]...
type ExecRoomProvisioner struct {
	Bin string
}

func (p ExecRoomProvisioner) CreateRoom(ctx context.Context, as Identity, room string, owners []string) (RoomInfo, error) {
	args := []string{"muc", "create", "--room", room}
	for _, o := range owners {
		args = append(args, "--owner", o)
	}
	out, err := ExecTroupe(p).run(ctx, as.environ(), args...)
	if err != nil {
		return RoomInfo{}, err
	}
	var info RoomInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return RoomInfo{}, fmt.Errorf("troupe muc create: parsing output: %w", err)
	}
	return info, nil
}

// missingRoomProvisioner stands in when LifecycleDeps names no RoomProvisioner
// and its Troupe is not an ExecTroupe to derive one from.
type missingRoomProvisioner struct{}

var errNoRoomProvisioner = errors.New("no room provisioner is configured")

func (missingRoomProvisioner) CreateRoom(context.Context, Identity, string, []string) (RoomInfo, error) {
	return RoomInfo{}, errNoRoomProvisioner
}

func (missingRoomProvisioner) Affiliate(context.Context, Identity, string, string, ...string) error {
	return errNoRoomProvisioner
}

func (p ExecRoomProvisioner) Affiliate(ctx context.Context, as Identity, room, affiliation string, jids ...string) error {
	args := []string{"muc", "affiliate", "--room", room, "--affiliation", affiliation}
	for _, j := range jids {
		args = append(args, "--jid", j)
	}
	_, err := ExecTroupe(p).run(ctx, as.environ(), args...)
	return err
}
