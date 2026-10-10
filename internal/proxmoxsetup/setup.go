// Package proxmoxsetup prepares a connection on the Proxmox host itself. It
// owns a separate API principal, binary, service and state from any runner agent.
package proxmoxsetup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tailscale/tailcat"
)

// LocalNetworkPath is where Proxmox keeps permissions for the node's own
// bridges. A token needs SDN.Use there to see a bridge and to attach a machine
// to it, which is a different thing from being able to read the node: root sees
// vmbr0 where a token without it is shown no bridges at all.
const LocalNetworkPath = "/sdn/zones/localnetwork"

const ProviderPrivileges = "Sys.Audit SDN.Use VM.Clone VM.Allocate VM.Audit VM.Config.Disk VM.Config.CPU VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt VM.GuestAgent.Unrestricted Datastore.Audit Datastore.AllocateSpace"

var bridgeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

type Template struct {
	VMID int    `json:"vmid"`
	Name string `json:"name"`
	Node string `json:"node"`
}

type Connection struct {
	Templates      []Template `json:"templates,omitempty"`
	Name           string     `json:"name"`
	Endpoint       string     `json:"endpoint"`
	Credential     string     `json:"credential"`
	CAPEM          string     `json:"ca_pem"`
	TailcatAddress string     `json:"tailcat_address"`
	// Bridge is the node's own active bridge, so a provider made by this setup
	// is saved with one that exists rather than the vmbr0 a form assumes.
	Bridge string `json:"bridge,omitempty"`
}

func (c Connection) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("proxmox setup: the Proxmox connection needs an HTTPS API address")
	}
	if c.Name == "" || len(c.Name) > 128 || c.Credential == "" {
		return errors.New("proxmox setup: the Proxmox host did not supply its name and API credential")
	}
	for _, template := range c.Templates {
		if template.VMID < 100 || template.VMID > 999999999 || template.Node == "" || len(template.Node) > 256 || len(template.Name) > 256 {
			return errors.New("proxmox setup: invalid template inventory")
		}
	}
	if c.Bridge != "" && !bridgeName.MatchString(c.Bridge) {
		return errors.New("proxmox setup: the Proxmox host supplied an invalid network bridge name")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.CAPEM)) {
		return errors.New("proxmox setup: the Proxmox host did not supply its trusted certificate")
	}
	if _, err := tailcat.ParseAddr(tailcat.Addr(c.TailcatAddress)); err != nil {
		return errors.New("proxmox setup: the Proxmox host did not supply a complete Tailcat address")
	}
	return nil
}

// InstanceKey keeps repeated onboarding for one controller on the same service
// and credential, without sharing the runner agent's state or executable.
func InstanceKey(controller string) string {
	identity := strings.TrimRight(controller, "/")
	if address, ok := strings.CutPrefix(identity, "tailcat://"); ok {
		if info, err := tailcat.ParseAddr(tailcat.Addr(address)); err == nil {
			// Relay changes must not install a second service for the same controller.
			identity = "tailcat:" + info.ServerPublic.String()
		}
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:6])
}

type Host struct {
	ReadFile  func(string) ([]byte, error)
	WriteFile func(string, []byte, os.FileMode) error
	Run       func(context.Context, string, ...string) ([]byte, error)
}

// Prepare deliberately receives all host I/O so tests never tune the test host
// or invoke its Proxmox commands. The caller creates a private state directory.
func Prepare(ctx context.Context, h Host, dir, key string) (*Connection, error) {
	ca, err := h.ReadFile("/etc/pve/pve-root-ca.pem")
	if err != nil {
		return nil, errors.New("proxmox setup: run this command as root on the Proxmox host; its cluster CA could not be read")
	}
	leaf, err := h.ReadFile("/etc/pve/local/pveproxy-ssl.pem")
	if errors.Is(err, os.ErrNotExist) {
		leaf, err = h.ReadFile("/etc/pve/local/pve-ssl.pem")
	}
	if err != nil {
		return nil, errors.New("proxmox setup: the Proxmox API certificate could not be read")
	}
	block, _ := pem.Decode(leaf)
	if block == nil {
		return nil, errors.New("proxmox setup: the Proxmox API certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("proxmox setup: the Proxmox API certificate could not be parsed")
	}
	nameBytes, err := h.ReadFile("/etc/hostname")
	if err != nil {
		return nil, err
	}
	name := strings.Split(strings.TrimSpace(string(nameBytes)), ".")[0]
	if name == "" {
		return nil, errors.New("proxmox setup: the Proxmox host name is empty")
	}
	host := endpointHost(cert, name)
	if host == "" {
		return nil, errors.New("proxmox setup: the Proxmox API certificate needs a DNS name or IP subject alternative name that is not localhost; " +
			"give the node a certificate for its real name or address, then rerun setup")
	}
	c := &Connection{Name: "proxmox-" + name, Endpoint: "https://" + net.JoinHostPort(host, "8006"), CAPEM: string(ca)}
	// Prefer the cluster CA across certificate renewal. A custom certificate is
	// trusted from the root-owned local file when that CA does not sign it.
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
		c.CAPEM += "\n" + string(leaf)
	}
	tokenPath := filepath.Join(dir, "credential")
	if raw, err := h.ReadFile(tokenPath); err == nil {
		c.Credential = strings.TrimSpace(string(raw))
		if c.Credential == "" {
			return nil, errors.New("proxmox setup: the saved Proxmox credential is empty; restore it before retrying setup")
		}
		return c, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	user, role := "zoomies-"+key+"@pve", "Zoomies-"+key
	// Recover partial setup with existing dedicated objects. Failures are only
	// ignored when the corresponding object is confirmed to exist.
	ensure := func(kind, id string, args ...string) error {
		if _, err := h.Run(ctx, "pveum", append([]string{kind, "add", id}, args...)...); err == nil {
			return nil
		}
		raw, err := h.Run(ctx, "pveum", kind, "list", "--output-format", "json")
		if err != nil {
			return fmt.Errorf("proxmox setup: cannot create the dedicated Proxmox %s", kind)
		}
		var rows []map[string]any
		if json.Unmarshal(raw, &rows) != nil {
			return fmt.Errorf("proxmox setup: cannot inspect the dedicated Proxmox %s", kind)
		}
		idField := "userid"
		if kind == "role" {
			idField = "roleid"
		}
		for _, row := range rows {
			if row[idField] == id {
				return nil
			}
		}
		return fmt.Errorf("proxmox setup: cannot create the dedicated Proxmox %s", kind)
	}
	if err := ensure("user", user); err != nil {
		return nil, err
	}
	privs := ProviderPrivileges
	if err := ensure("role", role, "--privs", privs); err != nil {
		return nil, err
	}
	// Proxmox users are cluster-wide, while gateway state lives on one node.
	// A second node must get its own token rather than rotate the first node's.
	tokenName := "provider-" + InstanceKey(name)
	tokenID := user + "!" + tokenName
	for _, path := range []string{"/vms", "/storage", "/nodes"} {
		if _, err := h.Run(ctx, "pveum", "acl", "modify", path, "--users", user, "--roles", role); err != nil {
			return nil, errors.New("proxmox setup: cannot grant the dedicated Proxmox user its provider permissions")
		}
	}
	raw, err := h.Run(ctx, "pveum", "user", "token", "add", user, tokenName, "--privsep", "1", "--output-format", "json")
	if err != nil {
		// A crashed first attempt may have created a token whose secret was
		// never saved. Create another token; never revoke a possibly live one.
		suffix := make([]byte, 8)
		if _, err := rand.Read(suffix); err != nil {
			return nil, err
		}
		tokenName += "-" + hex.EncodeToString(suffix)
		tokenID = user + "!" + tokenName
		raw, err = h.Run(ctx, "pveum", "user", "token", "add", user, tokenName, "--privsep", "1", "--output-format", "json")
		if err != nil {
			return nil, errors.New("proxmox setup: cannot create the dedicated Proxmox token; check Proxmox permissions and retry")
		}
	}
	var token struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &token) != nil || token.Value == "" {
		return nil, errors.New("proxmox setup: proxmox did not return the new API token")
	}
	c.Credential = tokenID + "=" + token.Value
	if err := h.WriteFile(tokenPath, []byte(c.Credential), 0600); err != nil {
		return nil, err
	}
	if err := GrantToken(ctx, h, tokenID, role); err != nil {
		return nil, err
	}
	return c, nil
}

// GrantToken gives the token, and the user it belongs to, the role on every
// path the provider works under. Both halves matter: the token is created with
// privilege separation on, so what it may do is the intersection of its own
// grants and its user's, and a path granted to one of them alone is granted to
// neither. It runs on every setup, so a role or path a newer release needs
// reaches a connection made by an older one.
func GrantToken(ctx context.Context, h Host, tokenID, role string) error {
	user, _, _ := strings.Cut(tokenID, "!")
	for _, path := range []string{"/vms", "/storage", "/nodes"} {
		if _, err := h.Run(ctx, "pveum", "acl", "modify", path, "--users", user, "--roles", role); err != nil {
			return errors.New("proxmox setup: cannot grant the dedicated Proxmox user its provider permissions")
		}
		if _, err := h.Run(ctx, "pveum", "acl", "modify", path, "--tokens", tokenID, "--roles", role); err != nil {
			return errors.New("proxmox setup: cannot grant the dedicated Proxmox token its provider permissions")
		}
	}
	// Best effort: a Proxmox without the local network zone has no such path and
	// needs no grant on it. Whether it was needed is what the check after says.
	_, _ = h.Run(ctx, "pveum", "acl", "modify", LocalNetworkPath, "--users", user, "--roles", role)
	_, _ = h.Run(ctx, "pveum", "acl", "modify", LocalNetworkPath, "--tokens", tokenID, "--roles", role)
	return nil
}

// Command installs only a private copy of the matching binary; the general
// installer may replace an agent executable or re-enrol a running host.
func Command(controller, id, token, tag string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	dir := "/var/lib/zoomies-proxmox/" + InstanceKey(controller)
	return "if [ \"$(id -u)\" -eq 0 ]; then zoomies_proxmox_sudo=''; else zoomies_proxmox_sudo='sudo'; fi\n" +
		"$zoomies_proxmox_sudo bash <<'ZOOMIES_PROXMOX_SETUP'\nset -eu\numask 077\n" +
		"dir=" + quote(dir) + "\nmkdir -p \"$dir\"\n" +
		"if [ -f \"$dir/completed-" + InstanceKey(id+token) + "\" ] && [ -x \"$dir/zoomies\" ]; then\n  \"$dir/zoomies\" providers connect-proxmox --controller " + quote(controller) + " --setup-id " + quote(id) + " --token " + quote(token) + "\n  exit 0\nfi\n" +
		"case $(uname -m) in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported host architecture'; exit 1;; esac\n" +
		"asset=zoomies_linux_$arch\nbase=" + quote("https://github.com/eyupio/zoomies/releases/download/"+tag) + "\n" +
		"tmp=$(mktemp -d \"$dir/download.XXXXXX\")\ntrap 'rm -rf \"$tmp\"' EXIT\n" +
		"curl --proto '=https' --proto-redir '=https' -fsSL \"$base/$asset\" -o \"$tmp/$asset\"\n" +
		"curl --proto '=https' --proto-redir '=https' -fsSL \"$base/checksums.txt\" -o \"$tmp/checksums.txt\"\n" +
		"(cd \"$tmp\"; awk -v asset=\"$asset\" '$2 == asset || $2 == \"*\" asset { print; found=1 } END { if (!found) exit 1 }' checksums.txt > selected; sha256sum -c selected)\n" +
		"chmod 700 \"$tmp/$asset\"\n" +
		"\"$tmp/$asset\" providers connect-proxmox --controller " + quote(controller) + " --setup-id " + quote(id) + " --token " + quote(token) + "\n" +
		"ZOOMIES_PROXMOX_SETUP"
}

// ConnectScriptURL is where connect-proxmox.sh is served; the site workflow
// copies the file from the repository root, as it does install.sh.
const ConnectScriptURL = "https://zoomies.sh/connect-proxmox.sh"

// ShortCommand is Command's two-line form: the same private, checksum-verified
// download, done by the hosted connect-proxmox.sh so the operator pastes a line
// they can read instead of a script. Command stays as the full text for a host
// that cannot reach zoomies.sh, and for anyone who wants to read every step
// before running it.
func ShortCommand(controller, id, token, tag string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	return "if [ \"$(id -u)\" -eq 0 ]; then zoomies_sudo=''; else zoomies_sudo='sudo'; fi\n" +
		"curl --proto '=https' --proto-redir '=https' -fsSL " + ConnectScriptURL + " | $zoomies_sudo sh -s -- " +
		"--version " + quote(tag) + " --key " + quote(InstanceKey(controller)) +
		" --controller " + quote(controller) + " --setup-id " + quote(id) + " --token " + quote(token)
}

// DiscoverTemplates reads cluster inventory without changing any guests.
func DiscoverTemplates(ctx context.Context, h Host) ([]Template, error) {
	raw, err := h.Run(ctx, "pvesh", "get", "/cluster/resources", "--type", "vm", "--output-format", "json")
	if err != nil {
		return nil, errors.New("proxmox setup: cannot list VM templates")
	}
	var rows []struct {
		VMID     int    `json:"vmid"`
		Name     string `json:"name"`
		Node     string `json:"node"`
		Type     string `json:"type"`
		Template int    `json:"template"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.New("proxmox setup: invalid VM inventory")
	}
	var templates []Template
	for _, row := range rows {
		if row.Type == "qemu" && row.Template == 1 && row.VMID >= 100 && row.Node != "" {
			templates = append(templates, Template{VMID: row.VMID, Name: row.Name, Node: row.Node})
		}
	}
	sort.Slice(templates, func(i, j int) bool { return templates[i].VMID < templates[j].VMID })
	return templates, nil
}

// endpointHost picks the name the controller will dial the node by. A Proxmox
// certificate lists localhost among its names, and the first one in the list
// is as likely to be that as the node's own, but no name that only means "this
// machine" can be verified from anywhere else, and it is the wrong thing to
// save: the provider fails its first check and the CA that was sent along
// cannot help. So loopback names are never chosen, whatever order they come in.
func endpointHost(cert *x509.Certificate, node string) string {
	for _, name := range cert.DNSNames {
		if !strings.Contains(name, "*") && !loopbackName(name) {
			return name
		}
	}
	for _, name := range cert.DNSNames {
		if strings.HasPrefix(name, "*.") && !loopbackName(node+strings.TrimPrefix(name, "*")) {
			return node + strings.TrimPrefix(name, "*")
		}
	}
	for _, ip := range cert.IPAddresses {
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}

func loopbackName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "localhost" || name == "localhost.localdomain" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
