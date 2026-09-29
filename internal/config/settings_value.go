package config

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reading and writing one setting on a Config, by its dotted key.
//
// The walk is by reflection over the yaml tags rather than a closure per key,
// for the reason the registry itself exists: two hand-written halves drift, and
// a getter that was forgotten is a setting that reports the wrong value rather
// than one that fails to compile. The yaml tag is already the name of the key
// in the file, so the field a key names is not a mapping anybody has to keep --
// it is the same fact the file format is built on.

// Value returns the value of one setting in the shape the API and the database
// carry: a string for a duration, a bool or nil for an optional bool, a list
// for a list, and the plain value for everything else.
//
// A secret's value is returned as it is. Callers that render for a person go
// through SettingsView, which never carries one.
func (c *Config) Value(key string) (any, error) {
	s, ok := LookupSetting(key)
	if !ok {
		return nil, &SettingError{key, "is not a setting this build knows about"}
	}
	f, ok := fieldFor(c, key)
	if !ok {
		return nil, &SettingError{key, "is registered but names no field; this is a bug in Zoomies"}
	}
	return encode(s, f), nil
}

// SetValue parses value and assigns it, returning what the setting was before.
//
// It never mutates a slice or map the caller already handed out: a list is
// rebuilt, which is what makes it safe to call inside config.Live's shallow
// copy, where the old snapshot may still be being read.
func (c *Config) SetValue(key string, value any) (before any, err error) {
	s, ok := LookupSetting(key)
	if !ok {
		return nil, &SettingError{key, "is not a setting this build knows about"}
	}
	f, ok := fieldFor(c, key)
	if !ok {
		return nil, &SettingError{key, "is registered but names no field; this is a bug in Zoomies"}
	}
	before = encode(s, f)
	if err := decode(s, f, value); err != nil {
		return nil, err
	}
	return before, nil
}

// SetValueString parses a value written as text -- from an environment
// variable, a command line or a database row -- and assigns it.
func (c *Config) SetValueString(key, raw string) (before any, err error) {
	s, ok := LookupSetting(key)
	if !ok {
		return nil, &SettingError{key, "is not a setting this build knows about"}
	}
	v, err := parseString(s, raw)
	if err != nil {
		return nil, err
	}
	return c.SetValue(key, v)
}

// SettingKeys returns every dotted key the Config struct actually has, found by
// walking it. It is what the registry is checked against, so a field added
// without a row fails a test rather than quietly having no environment
// variable and no place in the UI.
func SettingKeys() []string {
	var out []string
	walk(reflect.ValueOf(&Config{}).Elem(), "", func(path string, _ reflect.Value) {
		out = append(out, path)
	})
	return out
}

// fieldFor resolves a dotted key to the settable field behind it.
func fieldFor(c *Config, key string) (reflect.Value, bool) {
	v := reflect.ValueOf(c).Elem()
	for _, part := range strings.Split(key, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		next, ok := childNamed(v, part)
		if !ok {
			return reflect.Value{}, false
		}
		v = next
	}
	return v, true
}

func childNamed(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if yamlName(f) == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// walk visits every leaf field, which is every field that is not a plain
// struct: a duration is a struct-free int64, a map and a slice are leaves, and
// only the grouping structs (Server, TLS, Agent…) are descended into.
func walk(v reflect.Value, prefix string, fn func(path string, field reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := yamlName(f)
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		field := v.Field(i)
		if field.Kind() == reflect.Struct && field.Type() != reflect.TypeOf(time.Duration(0)) {
			walk(field, path, fn)
			continue
		}
		fn(path, field)
	}
}

func yamlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	return name
}

// encode turns a field into the shape the wire and the database carry.
func encode(s Setting, f reflect.Value) any {
	switch s.Kind {
	case KindDuration:
		return TidyDuration(time.Duration(f.Int()))
	case KindOptionalBool:
		if f.IsNil() {
			return nil
		}
		return f.Elem().Bool()
	case KindBool:
		return f.Bool()
	case KindInt:
		return int(f.Int())
	case KindFloat:
		return f.Float()
	case KindStrings:
		out := make([]string, f.Len())
		for i := range out {
			out[i] = f.Index(i).String()
		}
		return out
	case KindLabels:
		out := make(map[string]string, f.Len())
		for _, k := range f.MapKeys() {
			out[k.String()] = f.MapIndex(k).String()
		}
		return out
	default: // KindString, KindEnum, and TLSMode, which is a named string.
		return f.String()
	}
}

// decode parses one value and assigns it. The messages are the ones an operator
// reads, so they say what shape was wanted and give an example of it.
func decode(s Setting, f reflect.Value, value any) error {
	fail := func(format string, args ...any) error {
		return &SettingError{s.Key, fmt.Sprintf(format, args...)}
	}

	switch s.Kind {
	case KindDuration:
		text, ok := value.(string)
		if !ok {
			return fail("%v is not a duration; write it as text, like 30s, 5m or 2h", value)
		}
		text = strings.TrimSpace(text)
		d, err := time.ParseDuration(text)
		if err != nil && s.Key == "runners.docker_wait" {
			// ZOOMIES_DOCKER_WAIT named the runner image's own whole-seconds
			// wait for a Docker daemon long before this became a fleet
			// setting, and a pool's own env still writes it that way. An
			// operator -- or a runner's own environment, inherited by an
			// embedded controller -- who already has "120" set there is
			// carrying that contract forward, not making a mistake, so it is
			// read as seconds rather than refused.
			if n, serr := strconv.ParseInt(text, 10, 64); serr == nil {
				d, err = time.Duration(n)*time.Second, nil
			}
		}
		if err != nil {
			return fail("%q is not a duration (try 30s, 5m, 2h)", text)
		}
		if d < 0 {
			return fail("%q cannot be negative; use 0 to switch it off", text)
		}
		// Zero is always allowed: switching a timer off is a real answer. The
		// floor refuses what is technically a duration and practically an
		// outage.
		if s.Floor > 0 && d > 0 && d < s.Floor {
			return fail("%s is too short; the smallest useful value is %s", d, s.Floor)
		}
		f.SetInt(int64(d))

	case KindOptionalBool:
		if value == nil {
			f.Set(reflect.Zero(f.Type()))
			return nil
		}
		b, err := asBool(value)
		if err != nil {
			return fail("%v is not true, false or unset", value)
		}
		f.Set(reflect.ValueOf(&b))

	case KindBool:
		b, err := asBool(value)
		if err != nil {
			return fail("%v is not a boolean (use true or false)", value)
		}
		f.SetBool(b)

	case KindInt:
		n, err := asInt(value)
		if err != nil {
			return fail("%v is not a whole number", value)
		}
		// Every whole number in this configuration counts something -- runners,
		// attempts, megabytes -- so none of them can be negative.
		if n < 0 {
			return fail("%d cannot be negative", n)
		}
		f.SetInt(int64(n))

	case KindFloat:
		n, err := asFloat(value)
		if err != nil {
			return fail("%v is not a number", value)
		}
		// The one fractional setting measures a share of a machine, so a
		// negative one is not a smaller share but a different question.
		if n < 0 {
			return fail("%v cannot be negative", n)
		}
		f.SetFloat(n)

	case KindEnum:
		text, ok := value.(string)
		if !ok {
			return fail("expected a string, got %T", value)
		}
		text = strings.ToLower(strings.TrimSpace(text))
		found := false
		for _, c := range s.Choices {
			if c == text {
				found = true
				break
			}
		}
		if !found {
			// Named, not enumerated: "is not a log level" is what somebody
			// searches for and what they will recognise, and the list of what
			// it could have been comes after it.
			return fail("%q is not a %s; use %s", text, strings.ToLower(s.Label), orList(s.Choices))
		}
		f.SetString(text)

	case KindStrings:
		list, err := asStrings(value)
		if err != nil {
			return fail("%v is not a list of values", value)
		}
		// A fresh slice: the snapshot this field belongs to may be a shallow
		// copy of one somebody is still reading.
		out := reflect.MakeSlice(f.Type(), len(list), len(list))
		for i, item := range list {
			out.Index(i).SetString(item)
		}
		if len(list) == 0 {
			out = reflect.Zero(f.Type())
		}
		f.Set(out)

	case KindLabels:
		pairs, err := asLabels(value)
		if err != nil {
			return fail("%s", err)
		}
		out := reflect.MakeMapWithSize(f.Type(), len(pairs))
		for k, v := range pairs {
			out.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(v))
		}
		if len(pairs) == 0 {
			out = reflect.Zero(f.Type())
		}
		f.Set(out)

	default: // KindString
		text, ok := value.(string)
		if !ok {
			return fail("%v is not text", value)
		}
		f.SetString(strings.TrimSpace(text))
	}
	return nil
}

// parseString reads a value written as text. It is what an environment
// variable, a `zoomies config set` argument and a database row all go through,
// so one spelling means one thing in all three.
func parseString(s Setting, raw string) (any, error) {
	fail := func(format string, args ...any) (any, error) {
		return nil, &SettingError{s.Key, fmt.Sprintf(format, args...)}
	}
	switch s.Kind {
	case KindOptionalBool:
		// Empty text is how "nothing has been said about this" is written, and
		// for a tri-state that is an answer rather than a mistake: it puts the
		// setting back to being derived from the ones it follows.
		if strings.TrimSpace(raw) == "" {
			return nil, nil
		}
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fail("%q is not true, false or empty", raw)
		}
		return b, nil
	case KindBool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fail("%q is not a boolean (use true or false)", raw)
		}
		return b, nil
	case KindInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fail("%q is not an integer", raw)
		}
		return n, nil
	case KindFloat:
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return fail("%q is not a number", raw)
		}
		return n, nil
	case KindStrings:
		return splitList(raw), nil
	case KindLabels:
		m, err := parseKV(raw)
		if err != nil {
			return fail("%s", err)
		}
		return m, nil
	default: // strings, enums and durations are carried as text already.
		return raw, nil
	}
}

// Text renders a value the way parseString reads it back, so what the database
// stores round-trips through what an operator would have typed.
//
// It dispatches on the setting's kind rather than on the value's Go type. A
// value that arrived as JSON is []any and map[string]any rather than []string
// and map[string]string, and a type switch that fell through to fmt.Sprint for
// those wrote "[openid profile email groups]" into the database -- a list that
// read back as one item whose name was the whole list.
func Text(s Setting, value any) string {
	if value == nil {
		return ""
	}
	switch s.Kind {
	case KindStrings:
		list, err := asStrings(value)
		if err != nil {
			return ""
		}
		escaped := make([]string, len(list))
		for i, item := range list {
			escaped[i] = escapeList(item)
		}
		return strings.Join(escaped, ",")
	case KindLabels:
		pairs, err := asLabels(value)
		if err != nil {
			return ""
		}
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		// Sorted, so the same map always renders the same text: a row that
		// re-encodes differently every restart would look like a change.
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, escapeList(k)+"="+escapeList(pairs[k]))
		}
		return strings.Join(parts, ",")
	case KindDuration:
		switch v := value.(type) {
		case string:
			return v
		case time.Duration:
			return TidyDuration(v)
		}
	case KindBool, KindOptionalBool:
		if b, err := asBool(value); err == nil {
			return strconv.FormatBool(b)
		}
		return ""
	case KindInt:
		if n, err := asInt(value); err == nil {
			return strconv.Itoa(n)
		}
		return ""
	case KindFloat:
		if n, err := asFloat(value); err == nil {
			// The shortest text that reads back as the same number, so 2
			// stores as "2" rather than as "2.0" and a half core keeps its
			// half.
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// TidyDuration is a duration as an operator writes it: 168h, not 168h0m0s.
//
// Go's own rendering is exact and unreadable past an hour, and it is what both
// the settings page and the stored row would otherwise carry -- so a retention
// window somebody typed as "720h" came back as "720h0m0s" and looked like
// something the product had decided rather than something they had said. The
// result still parses, which is the only constraint: it is what goes into the
// database and what comes back out of it.
func TidyDuration(d time.Duration) string {
	s := d.String()
	// Only a whole trailing unit is dropped, never digits: "30s" must not
	// become "3". Go writes the units largest first, so a zero seconds can only
	// follow a minutes unit, and a zero minutes can only follow an hours one.
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// orList reads the way somebody would say it: "debug, info, warn or error".
func orList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

func asBool(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(v))
	}
	return false, fmt.Errorf("not a boolean")
}

func asInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		// JSON has one number type, so an integer arrives as a float. A
		// fractional one is a mistake rather than a rounding opportunity.
		if v != float64(int(v)) {
			return 0, fmt.Errorf("not a whole number")
		}
		return int(v), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	}
	return 0, fmt.Errorf("not a number")
}

func asFloat(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	return 0, fmt.Errorf("not a number")
}

func asStrings(value any) ([]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return v, nil
	case string:
		return splitList(v), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("not a list of text")
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("not a list")
}

func asLabels(value any) (map[string]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case map[string]string:
		return v, nil
	case string:
		return parseKV(v)
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("the value of %q is not text", k)
			}
			out[k] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("not a set of key=value labels")
}

// Redacted renders the configuration as a nested object shaped like the file,
// with every secret absent.
//
// It is generated from the registry, and a secret cannot appear in it: the
// registry says which keys are credentials, and this writes `<key>_configured`
// -- a boolean -- for each of them instead of the value. A hand-written
// version got the same safety from being hand-written, which held right up
// until somebody added a key and forgot. It is what the settings page, the
// support bundle and a backup's manifest all carry, so the three cannot
// disagree about what is safe to show.
func Redacted(c *Config) map[string]any {
	out := map[string]any{}
	for _, st := range Settings() {
		v, err := c.Value(st.Key)
		if err != nil {
			continue
		}
		if st.Secret {
			SetNested(out, st.Key+"_configured", Text(st, v) != "")
			continue
		}
		SetNested(out, st.Key, v)
	}
	return out
}

// SetNested writes a dotted key into a tree of maps.
func SetNested(into map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	for _, part := range parts[:len(parts)-1] {
		child, ok := into[part].(map[string]any)
		if !ok {
			child = map[string]any{}
			into[part] = child
		}
		into = child
	}
	into[parts[len(parts)-1]] = value
}
