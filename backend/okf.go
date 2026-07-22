package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// OKF (Open Knowledge Format) bundle engine.
//
// A bundle is a directory tree of UTF-8 markdown files. Each non-reserved
// .md file is a Concept (a graph node); its kind is the free-text `type`
// frontmatter field. Markdown links between concepts are directed edges.
// Reserved filenames (index.md, log.md) are not concepts.

const (
	okfExt      = ".md"
	indexFile   = "index.md"
	logFile     = "log.md"
	frontDelim  = "---"
)

// Concept is one OKF knowledge document.
type Concept struct {
	ID          string   `json:"id"`          // path minus .md, forward-slashed
	Type        string   `json:"type"`        // free-text kind -> drives node color
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Resource    string   `json:"resource"`    // canonical URI of the underlying asset
	FQN         string   `json:"fqn,omitempty"` // fully qualified name of the asset
	UserManaged bool     `json:"user_managed,omitempty"`
	Tags        []string `json:"tags"`
	Timestamp   string   `json:"timestamp"`
	Body        string   `json:"body"`        // markdown after frontmatter
	Links       []string `json:"links"`       // resolved target concept IDs
}

type frontmatter struct {
	Type        string   `yaml:"type"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Resource    string   `yaml:"resource,omitempty"`
	FQN         string   `yaml:"fqn,omitempty"`
	UserManaged bool     `yaml:"user_managed,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	Timestamp   string   `yaml:"timestamp,omitempty"`
}

// GraphNode / GraphEdge / Graph are the serialized shapes consumed by the
// force-graph frontend.
type GraphNode struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Resource    string   `json:"resource"`
	FQN         string   `json:"fqn,omitempty"`
	UserManaged bool     `json:"user_managed,omitempty"`
	Tags        []string `json:"tags"`
}

type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind,omitempty"` // containment | lineage | definition | reference
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// ConceptDetail is a single concept plus its immediate neighbors.
type ConceptDetail struct {
	Concept   Concept     `json:"concept"`
	Neighbors []GraphNode `json:"neighbors"`
}

// OKFBundle is a handle to a bundle directory on disk.
type OKFBundle struct {
	root string
}

func NewOKFBundle(path string) *OKFBundle {
	if path == "" {
		path = "okf-bundle"
	}
	return &OKFBundle{root: path}
}

var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// safePath resolves a concept ID to an absolute .md path inside the bundle
// root, rejecting any path that escapes the root (traversal protection).
func (b *OKFBundle) safePath(id string) (string, error) {
	id = strings.TrimSuffix(strings.TrimPrefix(id, "/"), okfExt)
	if id == "" {
		return "", fmt.Errorf("empty concept id")
	}
	rootAbs, err := filepath.Abs(b.root)
	if err != nil {
		return "", err
	}
	full := filepath.Clean(filepath.Join(rootAbs, filepath.FromSlash(id)+okfExt))
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("concept id escapes bundle root: %q", id)
	}
	return full, nil
}

// idFromPath turns an absolute/relative .md file path into a bundle concept ID.
func (b *OKFBundle) idFromPath(path string) (string, error) {
	rootAbs, err := filepath.Abs(b.root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(strings.TrimSuffix(rel, okfExt)), nil
}

func isReserved(name string) bool {
	return name == indexFile || name == logFile
}

// splitFrontmatter separates a leading YAML frontmatter block (delimited by
// "---") from the markdown body. Returns the raw YAML and the body.
func splitFrontmatter(content string) (string, string) {
	s := strings.TrimPrefix(content, string(rune(0xFEFF))) // strip BOM
	if !strings.HasPrefix(s, frontDelim) {
		return "", content
	}
	rest := s[len(frontDelim):]
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return "", content
	}
	// Find the closing delimiter on its own line.
	lines := strings.Split(rest, "\n")
	var yamlLines []string
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == frontDelim {
			body := strings.Join(lines[i+1:], "\n")
			return strings.Join(yamlLines, "\n"), strings.TrimPrefix(body, "\n")
		}
		yamlLines = append(yamlLines, lines[i])
	}
	return "", content // no closing delimiter; treat all as body
}

// resolveLink converts a markdown href into a target concept ID relative to
// the linking concept. Returns "" for external links (http, mailto, etc.).
func resolveLink(href, fromID string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if i := strings.IndexAny(href, "#?"); i >= 0 {
		href = href[:i]
	}
	if href == "" {
		return ""
	}
	lower := strings.ToLower(href)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") || strings.Contains(href, "://") {
		return ""
	}
	href = strings.TrimSuffix(href, okfExt)

	var target string
	if strings.HasPrefix(href, "/") {
		target = strings.TrimPrefix(href, "/") // bundle-absolute
	} else {
		target = filepath.ToSlash(filepath.Join(filepath.Dir(fromID), href)) // relative
	}
	target = strings.TrimPrefix(target, "./")
	return target
}

// typedLink is a resolved body link plus the edge kind derived from the
// Relationships bullet label on its line.
type typedLink struct {
	Target string
	Kind   string
}

// edgeKindForLine classifies a body line into an edge kind by its
// Relationships bullet label. Anything else is a plain reference.
func edgeKindForLine(line string) string {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "- Parent:"):
		return "containment"
	case strings.HasPrefix(t, "- Derived from:"):
		return "lineage"
	case strings.HasPrefix(t, "- Defined by:"):
		return "definition"
	}
	return "reference"
}

// classifyLinks returns the de-duplicated resolved links found in a body,
// each tagged with its edge kind (first occurrence wins).
func classifyLinks(body, fromID string) []typedLink {
	seen := map[string]bool{}
	var out []typedLink
	for _, line := range strings.Split(body, "\n") {
		kind := edgeKindForLine(line)
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			if t := resolveLink(m[1], fromID); t != "" && t != fromID && !seen[t] {
				seen[t] = true
				out = append(out, typedLink{Target: t, Kind: kind})
			}
		}
	}
	return out
}

// extractLinks returns the de-duplicated resolved target IDs found in a body.
func extractLinks(body, fromID string) []string {
	typed := classifyLinks(body, fromID)
	out := make([]string, 0, len(typed))
	for _, tl := range typed {
		out = append(out, tl.Target)
	}
	return out
}

func (b *OKFBundle) parseContent(id, content string) Concept {
	yamlPart, body := splitFrontmatter(content)
	var fm frontmatter
	if yamlPart != "" {
		_ = yaml.Unmarshal([]byte(yamlPart), &fm)
	}
	title := fm.Title
	if title == "" {
		title = filepath.Base(id)
	}
	return Concept{
		ID:          id,
		Type:        fm.Type,
		Title:       title,
		Description: fm.Description,
		Resource:    fm.Resource,
		FQN:         fm.FQN,
		UserManaged: fm.UserManaged,
		Tags:        fm.Tags,
		Timestamp:   fm.Timestamp,
		Body:        body,
		Links:       extractLinks(body, id),
	}
}

// ListConcepts walks the bundle and parses every non-reserved markdown file.
func (b *OKFBundle) ListConcepts() ([]Concept, error) {
	if _, err := os.Stat(b.root); os.IsNotExist(err) {
		return nil, nil // empty bundle is valid
	}
	var concepts []Concept
	err := filepath.Walk(b.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != okfExt || isReserved(info.Name()) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		id, ierr := b.idFromPath(path)
		if ierr != nil {
			return ierr
		}
		concepts = append(concepts, b.parseContent(id, string(raw)))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk bundle: %w", err)
	}
	sort.Slice(concepts, func(i, j int) bool { return concepts[i].ID < concepts[j].ID })
	return concepts, nil
}

func conceptNode(c Concept) GraphNode {
	return GraphNode{
		ID: c.ID, Title: c.Title, Type: c.Type,
		Description: c.Description, Resource: c.Resource, FQN: c.FQN,
		UserManaged: c.UserManaged, Tags: c.Tags,
	}
}

// BuildGraph returns nodes for every concept and edges for every markdown
// link whose target is also a concept (dangling links are dropped so the
// force-graph never references a missing node).
func (b *OKFBundle) BuildGraph() (*Graph, error) {
	concepts, err := b.ListConcepts()
	if err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(concepts))
	for _, c := range concepts {
		exists[c.ID] = true
	}
	g := &Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	for _, c := range concepts {
		g.Nodes = append(g.Nodes, conceptNode(c))
		for _, tl := range classifyLinks(c.Body, c.ID) {
			if exists[tl.Target] {
				g.Edges = append(g.Edges, GraphEdge{Source: c.ID, Target: tl.Target, Kind: tl.Kind})
			}
		}
	}
	return g, nil
}

// ReadConcept loads and parses a single concept file without walking the
// bundle.
func (b *OKFBundle) ReadConcept(id string) (Concept, error) {
	path, err := b.safePath(id)
	if err != nil {
		return Concept{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Concept{}, err
	}
	cid, err := b.idFromPath(path)
	if err != nil {
		return Concept{}, err
	}
	return b.parseContent(cid, string(raw)), nil
}

// neighborsFromGraph returns id's immediate neighbors (out-links first, then
// in-links) from an already-built graph.
func neighborsFromGraph(g *Graph, id string) []GraphNode {
	byID := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{id: true}
	var neighbors []GraphNode
	for _, e := range g.Edges { // out-links
		if e.Source == id && !seen[e.Target] {
			seen[e.Target] = true
			neighbors = append(neighbors, byID[e.Target])
		}
	}
	for _, e := range g.Edges { // in-links
		if e.Target == id && !seen[e.Source] {
			seen[e.Source] = true
			neighbors = append(neighbors, byID[e.Source])
		}
	}
	return neighbors
}

// GetConcept loads one concept plus its immediate neighbors (out-links and
// in-links). It rebuilds the full graph; callers that already hold a cached
// graph should use ReadConcept + neighborsFromGraph instead.
func (b *OKFBundle) GetConcept(id string) (*ConceptDetail, error) {
	c, err := b.ReadConcept(id)
	if err != nil {
		return nil, err
	}
	g, err := b.BuildGraph()
	if err != nil {
		return nil, err
	}
	return &ConceptDetail{Concept: c, Neighbors: neighborsFromGraph(g, c.ID)}, nil
}

// Search returns nodes matching a case-insensitive query over id/title/
// description/tags, optionally filtered by exact type and exact tag.
func (b *OKFBundle) Search(query, typeFilter, tagFilter string) ([]GraphNode, error) {
	concepts, err := b.ListConcepts()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := []GraphNode{}
	for _, c := range concepts {
		cType := c.Type
		if cType == "" {
			cType = "Untyped" // match CatalogTypes' bucket for empty types
		}
		if typeFilter != "" && cType != typeFilter {
			continue
		}
		if tagFilter != "" && !containsString(c.Tags, tagFilter) {
			continue
		}
		if q != "" && !conceptMatches(c, q) {
			continue
		}
		out = append(out, conceptNode(c))
	}
	return out, nil
}

// WriteIndexes regenerates a reserved index.md navigation file in the bundle
// root and in every directory holding concepts, each listing its direct child
// concepts. Reserved files never become concepts or graph nodes.
func (b *OKFBundle) WriteIndexes() error {
	concepts, err := b.ListConcepts()
	if err != nil {
		return err
	}
	if len(concepts) == 0 {
		return nil
	}
	byDir := map[string][]Concept{}
	for _, c := range concepts {
		dir := filepath.ToSlash(filepath.Dir(c.ID))
		byDir[dir] = append(byDir[dir], c)
	}
	if _, ok := byDir["."]; !ok {
		byDir["."] = nil // always write a root index
	}
	for dir, children := range byDir {
		var sb strings.Builder
		label := dir
		if dir == "." {
			label = "bundle root"
		}
		sb.WriteString(fmt.Sprintf("# Index of %s\n\nGenerated on import — navigation aid, not a concept.\n", label))
		if len(children) > 0 {
			sb.WriteString("\n")
			for _, c := range children {
				sb.WriteString(fmt.Sprintf("- [%s](/%s) — %s\n", c.Title, c.ID, c.Type))
			}
		}
		path := filepath.Join(b.root, filepath.FromSlash(dir), indexFile)
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			return fmt.Errorf("write index %q: %w", path, err)
		}
	}
	return nil
}

// serializeConcept renders a concept back to OKF markdown (frontmatter + body).
func serializeConcept(c Concept) string {
	if c.Type == "" {
		c.Type = "Untyped"
	}
	fm := frontmatter{
		Type:        c.Type,
		Title:       c.Title,
		Description: c.Description,
		Resource:    c.Resource,
		FQN:         c.FQN,
		UserManaged: c.UserManaged,
		Tags:        c.Tags,
		Timestamp:   c.Timestamp,
	}
	y, _ := yaml.Marshal(&fm)
	body := strings.TrimLeft(c.Body, "\n")
	return frontDelim + "\n" + string(y) + frontDelim + "\n\n" + body + "\n"
}

// WriteConcept upserts a concept to disk atomically (temp file + rename),
// constrained to the bundle root.
func (b *OKFBundle) WriteConcept(c Concept) error {
	path, err := b.safePath(c.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create concept dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(serializeConcept(c)), 0o644); err != nil {
		return fmt.Errorf("write concept: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit concept: %w", err)
	}
	return nil
}

// DeleteConcept removes a concept file, constrained to the bundle root.
func (b *OKFBundle) DeleteConcept(id string) error {
	path, err := b.safePath(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func conceptMatches(c Concept, q string) bool {
	if strings.Contains(strings.ToLower(c.ID), q) ||
		strings.Contains(strings.ToLower(c.Title), q) ||
		strings.Contains(strings.ToLower(c.Description), q) ||
		strings.Contains(strings.ToLower(c.Type), q) ||
		strings.Contains(strings.ToLower(c.FQN), q) {
		return true
	}
	for _, t := range c.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}
