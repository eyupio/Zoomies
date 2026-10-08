package catalog

import (
	"bufio"
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// The problem-codes page is a sequence of sections, each with one table whose
// header names its columns. The columns differ by section -- a configuration
// code names its setting, a runtime code does not, some tables split "what it
// means" from "what to do" and some do not -- so a row is read by its header's
// names rather than by position, and a table that gains a column needs a name
// here and nothing else.
const (
	colCode     = "code"
	colSeverity = "severity"
	colSetting  = "setting"
	colWhatToDo = "what to do"
	colMeaning  = "what it means"
	colVerify   = "how to see it worked"
	colStatus   = "what the status page says"
)

// sectionCategories is what each section's codes are about. Every
// "Configuration:" heading is configuration; the rest are named one by one, and
// a heading not listed here fails the build, so a new section is placed on
// purpose rather than landing somewhere by default.
var sectionCategories = map[string]Category{
	"Runtime: hosts and installations":  CategoryReliability,
	"Runtime: pools, jobs and runners":  CategoryCapacity,
	"Runtime: infrastructure providers": CategoryReliability,
	"A provider's preflight":            CategoryConfiguration,
}

// codeOverrides place a code by what it is about when its section's category
// would mislead: a dangerous toggle is a security matter wherever it is
// documented, and the one piece of advice about money is cost.
var codeOverrides = []struct {
	prefix   string
	category Category
}{
	{"security.", CategorySecurity},
	{"oidc.", CategorySecurity},
	{"pool.dangerous", CategorySecurity},
	{"pool.cache_shared", CategorySecurity},
	{"jobs.label_advice", CategoryCost},
}

var codeCell = regexp.MustCompile("`([a-z0-9_.]+)`")

// ParseProblemTables reads every code row on the page into an entry, attaching
// the status page's sentence to the entry it repeats rather than adding one.
func ParseProblemTables(markdown []byte) ([]Entry, error) {
	var (
		entries  []Entry
		index    = map[string]int{}
		section  string
		headers  []string
		inStatus bool
	)
	sc := bufio.NewScanner(bytes.NewReader(markdown))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "## "):
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			headers = nil
			inStatus = false
		case strings.HasPrefix(line, "| ") && headers == nil:
			headers = splitRow(line)
			for i := range headers {
				headers[i] = strings.ToLower(headers[i])
			}
			if headers[0] != colCode {
				// A table that is not keyed by code, such as the severity legend.
				headers = nil
				continue
			}
			inStatus = len(headers) == 2 && headers[1] == colStatus
		case strings.HasPrefix(line, "| `") && headers != nil:
			cells := splitRow(line)
			if len(cells) != len(headers) {
				return nil, errors.New("catalog: a row in " + section + " has " + strconv.Itoa(len(cells)) + " cells under " + strconv.Itoa(len(headers)) + " headers: " + line)
			}
			m := codeCell.FindStringSubmatch(cells[0])
			if m == nil {
				return nil, errors.New("catalog: no code in row: " + line)
			}
			id := m[1]
			byName := map[string]string{}
			for i, h := range headers {
				byName[h] = cells[i]
			}
			if inStatus {
				i, ok := index[id]
				if !ok {
					return nil, errors.New("catalog: the status page names " + id + ", which no table above documents")
				}
				s := byName[colStatus]
				entries[i].StatusSentence = &s
				continue
			}
			if _, dup := index[id]; dup {
				return nil, errors.New("catalog: " + id + " is documented twice")
			}
			e, err := problemEntry(id, section, byName)
			if err != nil {
				return nil, err
			}
			index[id] = len(entries)
			entries = append(entries, e)
		}
	}
	return entries, sc.Err()
}

func problemEntry(id, section string, cells map[string]string) (Entry, error) {
	category, detection, err := placement(id, section)
	if err != nil {
		return Entry{}, err
	}
	meaning := cells[colMeaning]
	fix := cells[colWhatToDo]
	if meaning == "" {
		// A configuration row says what to do and that is also what it means.
		meaning = fix
	}
	if fix == "" {
		fix = meaning
	}
	anchor := slug(section)
	return Entry{
		ID:        id,
		Kind:      KindProblem,
		Title:     id,
		Category:  category,
		Severity:  strings.ToLower(strings.Trim(cells[colSeverity], "* ")),
		Detection: detection,
		Detects:   meaning,
		Fix:       fix,
		Verify:    nonEmpty(cells[colVerify]),
		Setting:   strings.Trim(cells[colSetting], "` "),
		DocsHTML:  siteBase + "/problem-codes/#" + anchor,
		DocsMD:    repoBase + "/problem-codes.md#" + anchor,
	}, nil
}

func placement(id, section string) (Category, Detection, error) {
	var category Category
	detection := DetectionRuntime
	switch {
	case strings.HasPrefix(section, "Configuration:"):
		category, detection = CategoryConfiguration, DetectionStatic
	case section == "A provider's preflight":
		category, detection = CategoryConfiguration, DetectionStatic
	default:
		c, ok := sectionCategories[section]
		if !ok {
			return "", "", errors.New("catalog: section " + section + " has no category; add it to sectionCategories")
		}
		category = c
	}
	for _, o := range codeOverrides {
		if strings.HasPrefix(id, o.prefix) {
			category = o.category
		}
	}
	return category, detection, nil
}

// splitRow splits a Markdown table row on its unescaped pipes and trims each
// cell, keeping an escaped pipe as the character it stands for.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	const placeholder = "\x00"
	line = strings.ReplaceAll(line, `\|`, placeholder)
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(strings.ReplaceAll(parts[i], placeholder, "|"))
	}
	return parts
}

var slugDrop = regexp.MustCompile(`[^a-z0-9 -]`)

// slug is the anchor Material gives a heading: lower case, punctuation gone,
// spaces as dashes.
func slug(heading string) string {
	s := strings.ToLower(heading)
	s = slugDrop.ReplaceAllString(s, "")
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
	return s
}
