package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// The database layer: stored rows applied onto a configuration, and a
// configuration's values encoded back into rows.
//
// This is the layer between the file and the environment, and the one an
// administrator changes from the UI. It is applied after the store is open and
// the encryption key is loaded, which is the earliest moment a sealed value can
// be read -- and which is why the three keys that get the store open and the
// key loaded cannot themselves live here.

// ApplyStored writes stored settings onto cfg and records that they came from
// the database.
//
// A row that this build cannot use does not stop startup. A downgrade is the
// ordinary way to meet one -- an operator who set something in a newer version
// and rolled back -- and refusing to boot over a setting the older binary never
// had would turn "the new feature is not available" into "the controller is
// down". Each one becomes a finding instead, so the row is still named and
// still recoverable, and the fleet keeps running on the value underneath.
//
// It never mutates a slice or map the previous snapshot shares: every list and
// label set is rebuilt, which is what makes this safe to call inside a
// config.Live update.
func ApplyStored(cfg *Config, rows []store.InstanceSetting, key *cryptox.Key) Findings {
	var findings Findings
	var unknown, unreadable []string

	for _, row := range rows {
		s, ok := LookupSetting(row.Key)
		if !ok || !s.Stored() {
			unknown = append(unknown, row.Key)
			continue
		}

		raw := row.Value
		if s.Secret {
			opened, err := openSecret(key, row.Value)
			if err != nil {
				unreadable = append(unreadable, row.Key)
				continue
			}
			raw = opened
		}

		if _, err := cfg.SetValueString(row.Key, raw); err != nil {
			var se *SettingError
			reason := err.Error()
			if errors.As(err, &se) {
				reason = se.Reason
			}
			findings = append(findings, Finding{
				Code:     "settings.stored_invalid",
				Severity: SeverityWarning,
				Setting:  row.Key,
				Title:    fmt.Sprintf("The stored value for %s cannot be used", row.Key),
				Detail: fmt.Sprintf("It %s, so this controller is running the value underneath it instead \u2014 "+
					"whichever of the configuration file and the built-in default applies.", reason),
				Fix: fmt.Sprintf("Set %s again on the settings page, or clear it with `zoomies config unset %s`.", row.Key, row.Key),
			})
			continue
		}
		cfg.note(row.Key, SourceDatabase)
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		findings = append(findings, Finding{
			Code:     "settings.stored_unknown",
			Severity: SeverityInfo,
			Title:    fmt.Sprintf("%s stored that this version does not have", countOf(len(unknown), "One setting is", "settings are")),
			Detail: "They are kept untouched, so a controller upgraded again picks them up where it left off: " +
				strings.Join(unknown, ", ") + ".",
			Fix: "Nothing, unless they were meant for this version \u2014 in which case check the spelling on the settings page.",
		})
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		findings = append(findings, Finding{
			Code:     "settings.stored_unreadable",
			Severity: SeverityError,
			Title: fmt.Sprintf("%s sealed with a key this controller does not have",
				countOf(len(unreadable), "A stored credential is", "stored credentials are")),
			Detail: "The key this controller loaded does not open " + strings.Join(unreadable, ", ") + ". " +
				"Starting anyway would run the fleet with credentials silently missing.",
			Fix: "Restore the encryption key these were sealed with, or clear them with `zoomies config unset " + unreadable[0] + "` and set them again.",
		})
	}
	return findings
}

// EncodeStored turns a setting's value into the row that holds it, sealing it
// when the setting is a credential.
func EncodeStored(s Setting, value any, key *cryptox.Key) (store.InstanceSetting, error) {
	text := Text(s, value)
	if !s.Secret {
		return store.InstanceSetting{Key: s.Key, Value: text}, nil
	}
	if key == nil {
		return store.InstanceSetting{}, &SettingError{s.Key,
			"is a credential and there is no encryption key to seal it with; set security.encryption_key_file and restart"}
	}
	sealed, err := key.SealString(text)
	if err != nil {
		return store.InstanceSetting{}, &SettingError{s.Key, "could not be sealed: " + err.Error()}
	}
	return store.InstanceSetting{
		Key:    s.Key,
		Value:  base64.StdEncoding.EncodeToString(sealed),
		Secret: true,
	}, nil
}

// openSecret reverses EncodeStored's sealing. The base64 and the seal are two
// separate failures with the same answer -- this row was written by an instance
// holding a different key -- so they are reported as one.
func openSecret(key *cryptox.Key, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	if key == nil {
		return "", errors.New("no encryption key")
	}
	sealed, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return key.OpenString(sealed)
}

// Rebuild re-layers a running configuration over the fleet's stored settings.
//
// Order is the whole of it: the file is already in cfg, the database goes on
// top of it, and the environment goes on top of the database. The environment
// is last deliberately. It is the way back in when a stored setting has locked
// an operator out of the interface that would let them fix it -- a bind address
// nothing can reach, an external URL that breaks the login redirect -- and a
// deployment that ships its configuration as environment variables, which is
// every containerised one, keeps working across the upgrade that introduced
// this table.
//
// The cost of that choice is that a value an administrator types can be
// invisibly overridden, so it is not left invisible: Config.Source says where
// each value came from, PinnedByEnvironment lists the ones the environment is
// holding, and the settings page refuses to pretend it can change them.
//
// normalize runs last, as it does after Load, so derived values are derived
// from the values that actually won.
//
// One derived value has to be forgotten first. Load derived the secure-cookie
// flag before the database was open, from the file and the environment alone,
// and normalize only derives a flag that is unset -- so a stored https external
// URL never reached it, and a controller served over https issued cookies
// without Secure. A flag no layer chose is noted at no source, so it is safe to
// clear; one the file, the environment or a row chose keeps its source and its
// value.
func (c *Config) Rebuild(rows []store.InstanceSetting, key *cryptox.Key) (Findings, error) {
	if c.Source("security.cookie_secure") == SourceDefault {
		c.Security.CookieSecure = nil
	}
	findings := ApplyStored(c, rows, key)
	if err := c.applyEnv(); err != nil {
		return findings, err
	}
	c.normalize()
	return findings, nil
}

// Effective is the configuration a deployment runs with, worked out the way
// its controller works it out at start -- the file, then the database, then
// the environment -- for a caller that is not that controller: the installer,
// upgrading it, which has to know whether the deployment runs runners before
// it can say which mounts it needs.
//
// The database is the answer to that, as it is to every setting; asking the
// deployment's .env instead would miss everything set on the settings page.
// env is the deployment's own environment, never this process's, which is the
// upgrade's and says nothing about the service. file is empty for a container
// deployment, which keeps no zoomies.yaml.
func Effective(file string, rows []store.InstanceSetting, key *cryptox.Key, env map[string]string) (*Config, Findings, error) {
	cfg := Default()
	if file != "" {
		if err := cfg.readFile(file); err != nil {
			return nil, nil, err
		}
	}
	findings := ApplyStored(cfg, rows, key)
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	if err := cfg.applyEnvFrom(lookup); err != nil {
		return nil, findings, err
	}
	cfg.normalize()
	return cfg, findings, nil
}

// PendingRestart lists the settings whose stored value is not what this process
// is running, because it was stored after this process started and needs a
// restart to take effect.
//
// It is the honest half of letting an administrator change a setting that
// cannot be applied live. The alternative the product used to offer was to
// refuse the edit and say "edit the file and restart", which left the operator
// to do by hand the one thing a settings page exists to do -- and left no
// record that anybody had wanted the change.
//
// A setting the environment is pinning is never pending. Its stored value will
// lose to the variable at the next start exactly as it loses now, so calling it
// "waiting for a restart" would promise a change that is never coming.
func PendingRestart(running *Config, rows []store.InstanceSetting, key *cryptox.Key) []string {
	// A copy with every layer re-applied is what the next start will look
	// like. Anything that differs from the running configuration is waiting.
	next := *running
	next.sources = running.copySources()
	if _, err := next.Rebuild(rows, key); err != nil {
		// The environment cannot have become unparseable since this process
		// read it, so there is nothing useful to say and nothing to report.
		return nil
	}

	var out []string
	for _, s := range StoredSettings() {
		if s.Live || running.Source(s.Key) == SourceEnvironment {
			continue
		}
		if !sameValue(running, &next, s.Key) {
			out = append(out, s.Key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return CompareKeys(out[i], out[j]) < 0 })
	return out
}

// sameValue compares two configurations at one key, through the same text a
// row would store, so "30s" and "30s" match however each was arrived at.
func sameValue(a, b *Config, key string) bool {
	s, ok := LookupSetting(key)
	if !ok {
		return true
	}
	av, aerr := a.Value(key)
	bv, berr := b.Value(key)
	if aerr != nil || berr != nil {
		return aerr != nil && berr != nil
	}
	return Text(s, av) == Text(s, bv)
}

// countOf reads as a sentence opening rather than as a number and a noun:
// "One setting stored that this version does not have", "Three settings
// stored...". A finding's title is prose, and "1 setting" in front of a verb is
// not.
func countOf(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// SeedKey is the row that records the one-time import of a configuration file
// into the database. It lives in the store's own settings table rather than in
// instance_settings, because it is a fact the product keeps about itself and
// not a setting anybody may change.
const SeedKey = "config.file_imported"

// SeedFromFile returns the rows that carry a configuration file's settings into
// the database, so an instance upgraded into this design keeps running exactly
// as it did and gains a settings page that works.
//
// Only what the file actually spells is taken. A key it never mentioned is left
// alone, because the defaults are computed on the host that reads them -- the
// database path from the state directory, the agent's capacity from the number
// of cores, its name from the machine -- and freezing one host's answers into
// rows would quietly make every later host inherit them.
//
// A key the environment is pinning is taken too. The value stored is the one
// the file gave, not the one the environment is currently winning with: the
// import is a copy of the file, and writing the environment's value would turn
// a temporary override into a permanent setting the moment somebody unset the
// variable.
func SeedFromFile(cfg *Config, key *cryptox.Key) ([]store.InstanceSetting, error) {
	fromFile := &Config{path: cfg.path}
	if cfg.path == "" {
		return nil, nil
	}
	doc, err := readFile(cfg.path)
	if err != nil {
		return nil, err
	}
	if err := decodeYAML(doc, fromFile); err != nil {
		return nil, err
	}

	var rows []store.InstanceSetting
	for _, spelled := range keysIn(doc) {
		s, ok := LookupSetting(spelled)
		if !ok || !s.Stored() {
			continue
		}
		value, err := fromFile.Value(spelled)
		if err != nil {
			continue
		}
		row, err := EncodeStored(s, value, key)
		if err != nil {
			// A credential with no key to seal it stays in the file, where it
			// still works. Refusing the whole import over one of them would
			// leave an upgraded instance with no settings at all.
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return CompareKeys(rows[i].Key, rows[j].Key) < 0 })
	return rows, nil
}

// SeedFinding says what the import did, so an operator who upgraded finds out
// from the startup output rather than from a settings page that has quietly
// changed hands.
func SeedFinding(path string, n int) Finding {
	return Finding{
		Code:     "settings.imported_from_file",
		Severity: SeverityInfo,
		Title:    fmt.Sprintf("%s moved into this fleet's database", countOf(n, "One setting from "+path+" has", "settings from "+path+" have")),
		Detail: "Settings now live in the database, so they can be changed on the settings page and kept. " +
			"The file is still read as the layer underneath them, so nothing has changed about what this controller is running.",
		Fix: "Nothing. The file can stay as it is; a value changed on the settings page takes precedence over it from now on.",
	}
}

// SettingForEnv is the stored setting a ZOOMIES_* variable sets, if it sets
// one. Bootstrap and local settings are not returned: they cannot live in the
// database, so a variable naming one is not a setting to move there.
func SettingForEnv(name string) (Setting, bool) {
	for _, s := range registry {
		if s.Env == name && s.Stored() {
			return s, true
		}
	}
	return Setting{}, false
}

// EnvImport is one variable ImportEnvironment turned into a row.
type EnvImport struct {
	Env     string
	Setting Setting
	// Text is the value as the settings page shows it; empty for a secret.
	Text string
}

// ImportEnvironment turns the ZOOMIES_* variables a deployment was started
// with into the rows that hold the same settings in its database.
//
// It is how a container deployment's settings leave its .env. Every variable
// is parsed as the controller would parse it, and one that does not parse
// stops the whole import: storing the rest would leave the deployment half in
// the database and half in its environment, with the half that failed the
// one nobody notices. Variables that are not stored settings -- the
// encryption key, the database path, Compose's own -- are returned in left,
// so a caller can say they stay where they are.
func ImportEnvironment(vars map[string]string, key *cryptox.Key) (rows []store.InstanceSetting, imported []EnvImport, left []string, err error) {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		s, ok := SettingForEnv(name)
		if !ok {
			if strings.HasPrefix(name, "ZOOMIES_") {
				left = append(left, name)
			}
			continue
		}
		candidate := Default()
		if _, err := candidate.SetValueString(s.Key, vars[name]); err != nil {
			var se *SettingError
			if errors.As(err, &se) {
				problems = append(problems, fmt.Sprintf("%s: %s", name, se.Reason))
			} else {
				problems = append(problems, fmt.Sprintf("%s: %s", name, err))
			}
			continue
		}
		value, err := candidate.Value(s.Key)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", name, err))
			continue
		}
		row, err := EncodeStored(s, value, key)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", name, err))
			continue
		}
		rows = append(rows, row)
		text := ""
		if !s.Secret {
			text = Text(s, value)
		}
		imported = append(imported, EnvImport{Env: name, Setting: s, Text: text})
	}
	if len(problems) > 0 {
		return nil, nil, left, fmt.Errorf("nothing was stored, because these would not be what the controller runs with:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	sort.Slice(rows, func(i, j int) bool { return CompareKeys(rows[i].Key, rows[j].Key) < 0 })
	return rows, imported, left, nil
}
