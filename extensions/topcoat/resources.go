package topcoat

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The resource model of the generated pages: the collections the API's
// operations describe by their REST paths and results, and the page each
// operation gets on one.

// The kinds of page an operation gets on its resource.
const (
	// pageList is GET C returning T[]: the resource's list.
	pageList = "list"
	// pageDetail is GET C/{p} returning T: an item's page.
	pageDetail = "detail"
	// pageCreate is POST C with an input or arguments: the new page.
	pageCreate = "create"
	// pageUpdate is PUT or PATCH C/{p} with an input: the edit page.
	pageUpdate = "update"
	// pageDelete is DELETE C/{p}: a button with a confirmation page.
	pageDelete = "delete"
	// pageAction is any other method on C/{p}/<verb>: a form on the item's
	// page.
	pageAction = "action"
)

// restSegment is one segment of an operation's path below /api: a word,
// or a parameter by the API's name.
type restSegment struct {
	word  string
	param string
}

// restSegments is an operation's path below /api as its segments, or why
// a page cannot serve it: a path a page path cannot spell, a segment that
// mixes a word and a parameter, a path that begins with a parameter or
// holds two in a row.
func restSegments(path string) ([]restSegment, string) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api"), "/")
	if trimmed == "" {
		return nil, "its path is the API's root"
	}
	var segs []restSegment
	for _, part := range strings.Split(trimmed, "/") {
		switch {
		case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && len(part) > 2 && !strings.ContainsAny(part[1:len(part)-1], "{}*"):
			if len(segs) == 0 {
				return nil, "its path begins with a parameter"
			}
			if segs[len(segs)-1].param != "" {
				return nil, "its path holds two parameters in a row"
			}
			segs = append(segs, restSegment{param: part[1 : len(part)-1]})
		case strings.ContainsAny(part, "{}*"):
			return nil, fmt.Sprintf("its path segment %s is not a word or a parameter alone", part)
		case !pageWord(part):
			return nil, fmt.Sprintf("its path segment %q is no page path's", part)
		default:
			segs = append(segs, restSegment{word: part})
		}
	}
	return segs, ""
}

// pageWord reports whether a path segment is a word a page path holds as
// it is: letters, digits and -._~, and not a dot segment.
func pageWord(word string) bool {
	if word == "" || word == "." || word == ".." {
		return false
	}
	for _, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("-._~", r):
		default:
			return false
		}
	}
	return true
}

// collectionKey names a collection by its words, each parameter between
// them `{}`: products/{}/reviews. Two paths of one collection that name
// their parameters differently share it.
func collectionKey(segs []restSegment) string {
	parts := make([]string, len(segs))
	for i, seg := range segs {
		parts[i] = seg.word
		if seg.param != "" {
			parts[i] = "{}"
		}
	}
	return strings.Join(parts, "/")
}

// spell is a path below /api as the API writes it.
func spell(segs []restSegment) string {
	parts := make([]string, len(segs))
	for i, seg := range segs {
		parts[i] = seg.word
		if seg.param != "" {
			parts[i] = "{" + seg.param + "}"
		}
	}
	return strings.Join(parts, "/")
}

// resourceNode is a collection the operations' paths name: a resource when
// it has a list or a detail, else only a step of the path to one under it,
// such as `products` when only `products/{productId}/reviews` is listed.
type resourceNode struct {
	key string
	// words are the collection's own words, after its parent's item:
	// [reviews].
	words    []string
	parent   *resourceNode
	children []*resourceNode
	// list, detail, create, update and delete are the operation of each
	// kind of page; actions are its item's actions, by verb.
	list, detail, create, update, del *classified
	actions                           []*classified
	// param is the page route's parameter of an item: project_id.
	param string
}

// isResource reports whether the collection has a page of its own to
// show: a list or a detail.
func (n *resourceNode) isResource() bool { return n.list != nil || n.detail != nil }

// chain is the collection's ancestors and itself, outermost first.
func (n *resourceNode) chain() []*resourceNode {
	if n.parent == nil {
		return []*resourceNode{n}
	}
	return append(n.parent.chain(), n)
}

// classified is an operation and the page it gets on its resource.
type classified struct {
	d    declared
	kind string
	node *resourceNode
	// verb is an action's word, after the item: archive.
	verb string
	segs []restSegment
}

// leftOut is an operation that gets no page, and why.
type leftOut struct {
	Operation string // project.me
	Reason    string
}

// pageForms are the forms a page submits through: the argument forms by
// their Args struct, and the input forms by their type.
type pageForms struct {
	args   map[string]*argForm
	inputs map[string]*form
}

// argsFormOf is the operation's argument form, or nil.
func (f pageForms) argsFormOf(d declared) *argForm {
	if d.endpoint.ArgsName == "" {
		return nil
	}
	return f.args[d.endpoint.ArgsName]
}

// inputFormOf is the form of the operation's input type, or nil.
func (f pageForms) inputFormOf(d declared) *form {
	if d.endpoint.Input == nil {
		return nil
	}
	name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(d.endpoint.Input.RustType, "Option<"), "types::"), ">")
	return f.inputs[name]
}

// classify groups the operations a page can call into resources, by their
// REST paths and results, and gives each its page:
//
//   - GET C returning T[] is C's list, and GET C/{p} returning T its
//     detail; either makes C a resource;
//   - POST C, with an input or arguments, is the resource's new page;
//   - PUT or PATCH C/{p} with an input is its edit page, DELETE C/{p} its
//     delete button, and any other method on C/{p}/<verb> an action, each
//     on the item's page, so each needs the resource's detail;
//   - a resource under another's item (C/{p}/D) is nested: its pages are
//     under the item's, and its list and new page a section of the item's
//     page when the parent has a detail.
//
// Every other operation is left out with its reason: a GET of one object
// on a path with no parameter, say. roots are the collections no item
// holds, sorted by key, each with its children.
func classify(schemas schemaSet, ops []declared, forms pageForms) (roots []*resourceNode, out []leftOut) {
	nodes := map[string]*resourceNode{}
	leave := func(d declared, reason string) {
		out = append(out, leftOut{Operation: d.endpoint.Namespace + "." + d.endpoint.Name, Reason: reason})
	}
	type pending struct {
		d    declared
		segs []restSegment
	}
	var changes []pending
	// The lists and details first: they make a collection a resource.
	for _, d := range ops {
		if requiresService(d.endpoint) {
			leave(d, "a browser holds no service credential (@requireService)")
			continue
		}
		segs, reason := restSegments(d.endpoint.Path)
		if reason != "" {
			leave(d, reason)
			continue
		}
		if !strings.EqualFold(d.endpoint.Method, "GET") {
			changes = append(changes, pending{d, segs})
			continue
		}
		ref := d.op.TypeRef
		last := segs[len(segs)-1]
		switch {
		case last.word != "" && ref.IsArray && !ref.IsArrayOfArrays && !ref.IsMap && schemas.objectType(ref.Name) != nil:
			node := ensureNode(nodes, segs)
			if reason := callable(schemas, d, forms, pageList); reason != "" {
				leave(d, reason)
			} else if node.list != nil {
				leave(d, "its resource's list is "+node.list.name())
			} else {
				node.list = &classified{d: d, kind: pageList, node: node, segs: segs}
			}
		case last.param != "" && !ref.IsArray && !ref.IsMap && schemas.objectType(ref.Name) != nil:
			node := ensureNode(nodes, segs[:len(segs)-1])
			if reason := callable(schemas, d, forms, pageDetail); reason != "" {
				leave(d, reason)
			} else if node.detail != nil {
				leave(d, "its resource's detail is "+node.detail.name())
			} else {
				node.detail = &classified{d: d, kind: pageDetail, node: node, segs: segs}
			}
		case last.word != "":
			leave(d, fmt.Sprintf("a GET of %s, a path that ends in a word, is a list only when it returns a list of records", spell(segs)))
		default:
			leave(d, fmt.Sprintf("a GET of %s, a path that ends in a parameter, is a detail only when it returns one record", spell(segs)))
		}
	}
	resource := func(segs []restSegment) *resourceNode {
		if node := nodes[collectionKey(segs)]; node != nil && node.isResource() {
			return node
		}
		return nil
	}
	// Then what changes a resource or acts on one of its items.
	for _, p := range changes {
		d, segs := p.d, p.segs
		method := strings.ToUpper(d.endpoint.Method)
		n := len(segs)
		last := segs[n-1]
		switch {
		case last.param != "":
			node := resource(segs[:n-1])
			switch {
			case node == nil:
				leave(d, fmt.Sprintf("no list or detail makes %s a resource", spell(segs[:n-1])))
			case node.detail == nil:
				leave(d, fmt.Sprintf("its resource, %s, has no detail page to hold it", spell(segs[:n-1])))
			case method == "PUT" || method == "PATCH":
				switch {
				case d.endpoint.Input == nil:
					leave(d, "a "+method+" without an input has no form to edit")
				case node.update != nil:
					leave(d, "its resource's edit page updates it through "+node.update.name())
				default:
					node.update = &classified{d: d, kind: pageUpdate, node: node, segs: segs}
				}
			case method == "DELETE":
				if node.del != nil {
					leave(d, "its resource's delete button is "+node.del.name())
				} else {
					node.del = &classified{d: d, kind: pageDelete, node: node, segs: segs}
				}
			default:
				leave(d, fmt.Sprintf("a %s of an item, with no word after it, is no action", method))
			}
		case method == "POST" && resource(segs) != nil:
			node := resource(segs)
			switch {
			case d.endpoint.Input == nil && len(d.endpoint.QueryArgs)+len(d.endpoint.BodyArgs) == 0:
				leave(d, "it takes nothing a form could fill")
			case node.create != nil:
				leave(d, "its resource's new page creates through "+node.create.name())
			default:
				node.create = &classified{d: d, kind: pageCreate, node: node, segs: segs}
			}
		case n >= 3 && segs[n-2].param != "" && resource(segs[:n-2]) != nil:
			node := resource(segs[:n-2])
			switch {
			case node.detail == nil:
				leave(d, fmt.Sprintf("its resource, %s, has no detail page to hold it", spell(segs[:n-2])))
			case last.word == "edit" || last.word == "delete":
				leave(d, fmt.Sprintf("its word, %s, names the item's %s page", last.word, last.word))
			case node.action(last.word) != nil:
				leave(d, fmt.Sprintf("the action %s of its resource is %s", last.word, node.action(last.word).name()))
			default:
				node.actions = append(node.actions, &classified{d: d, kind: pageAction, node: node, verb: last.word, segs: segs})
			}
		default:
			leave(d, fmt.Sprintf("no list or detail makes %s a resource, nor %s an action of one", spell(segs), method+" "+spell(segs)))
		}
	}
	// A page submits through a form, which update, delete, action and
	// create pages need; one without is left out.
	for _, key := range sortedKeys(nodes) {
		node := nodes[key]
		keep := func(c *classified) *classified {
			if c == nil {
				return nil
			}
			if reason := callable(schemas, c.d, forms, c.kind); reason != "" {
				leave(c.d, reason)
				return nil
			}
			return c
		}
		node.create, node.update, node.del = keep(node.create), keep(node.update), keep(node.del)
		var actions []*classified
		for _, action := range node.actions {
			if keep(action) != nil {
				actions = append(actions, action)
			}
		}
		node.actions = actions
	}
	for _, key := range sortedKeys(nodes) {
		node := nodes[key]
		if node.parent == nil {
			roots = append(roots, node)
		} else {
			node.parent.children = append(node.parent.children, node)
		}
	}
	nameParams(roots, nil)
	return roots, out
}

// name is the operation, namespace and name: project.getProject.
func (c *classified) name() string {
	return c.d.endpoint.Namespace + "." + c.d.endpoint.Name
}

// action is the item's action of verb, or nil.
func (n *resourceNode) action(verb string) *classified {
	for _, action := range n.actions {
		if action.verb == verb {
			return action
		}
	}
	return nil
}

// ensureNode is the node of the collection segs spell, made with its
// ancestors when new.
func ensureNode(nodes map[string]*resourceNode, segs []restSegment) *resourceNode {
	key := collectionKey(segs)
	if node := nodes[key]; node != nil {
		return node
	}
	// The collection's own words follow its parent's item parameter.
	start := 0
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i].param != "" {
			start = i + 1
			break
		}
	}
	node := &resourceNode{key: key}
	for _, seg := range segs[start:] {
		node.words = append(node.words, seg.word)
	}
	if start > 0 {
		node.parent = ensureNode(nodes, segs[:start-1])
	}
	nodes[key] = node
	return node
}

// callable is why a page cannot call the operation, or empty: a page
// submits through the operation's form, an argument form or, for an
// operation whose only argument is its input, the input's; a list or a
// detail with no argument but its path's builds its arguments from the
// route.
func callable(schemas schemaSet, d declared, forms pageForms, kind string) string {
	e := d.endpoint
	if forms.argsFormOf(d) != nil {
		return ""
	}
	switch {
	case len(e.QueryArgs)+len(e.BodyArgs) > 0, len(e.PathArgs) > 0 && e.Input != nil:
		return "its arguments have no form"
	case e.Input != nil:
		if forms.inputFormOf(d) == nil {
			return "its input has no form"
		}
		if kind != pageCreate {
			return "its arguments have no form"
		}
		return ""
	case len(e.PathArgs) > 0 && kind != pageList && kind != pageDetail:
		return "its arguments have no form"
	}
	// A route's value is read as an argument form reads a path's.
	for _, param := range e.PathArgs {
		if routeReader(schemas, d, param.Name) == "" {
			return fmt.Sprintf("its path argument %s is not a value a route holds", param.Name)
		}
	}
	return ""
}

// routeReader is the helper that reads a route's value as the JSON of the
// operation's argument name, as an argument form reads a path's: as_text,
// as_integer, as_number or as_boolean; empty for an argument a route
// cannot hold.
func routeReader(schemas schemaSet, d declared, name string) string {
	for _, arg := range d.op.Arguments {
		if arg.Name != name {
			continue
		}
		ref := arg.TypeRef
		if ref.IsArray || ref.IsMap || schemas.objectType(ref.Name) != nil {
			return ""
		}
		if schemas.enum(ref.Name) != nil {
			return "as_text"
		}
		switch schemas.leafOf(ref.Name) {
		case stringLeaf:
			return "as_text"
		case integerLeaf:
			return "as_integer"
		case numberLeaf:
			return "as_number"
		case booleanLeaf:
			return "as_boolean"
		}
	}
	return ""
}

// nameParams names each collection's item parameter in the page routes
// after its last word, singular: project_id for projects. A name its
// ancestors already use gets a number. A page route's parameter at one
// place is the same in every route through it, as the router requires.
func nameParams(nodes []*resourceNode, taken []string) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].key < nodes[j].key })
	for _, node := range nodes {
		base := snakeWord(singular(node.words[len(node.words)-1])) + "_id"
		name := base
		for i := 2; contains(taken, name); i++ {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		node.param = name
		nameParams(node.children, append(append([]string(nil), taken...), name))
	}
}

func contains(list []string, item string) bool {
	for _, value := range list {
		if value == item {
			return true
		}
	}
	return false
}

// snakeWord is a path word as a Rust identifier's part: lower case, each
// other character an underscore, never starting with a digit.
func snakeWord(word string) string {
	var b strings.Builder
	underscore := false
	for i, r := range word {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && !underscore {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			underscore = false
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			underscore = false
		default:
			if !underscore && b.Len() > 0 {
				b.WriteByte('_')
				underscore = true
			}
		}
	}
	text := strings.Trim(b.String(), "_")
	if text == "" || text[0] >= '0' && text[0] <= '9' {
		text = "r_" + text
	}
	return text
}

// singular is a plural word made singular by the English rules a path's
// words mostly follow, for a label and a parameter's name: projects is
// project, categories category, addresses address.
func singular(word string) string {
	lower := strings.ToLower(word)
	switch {
	case strings.HasSuffix(lower, "ies") && len(word) > 3:
		return word[:len(word)-3] + "y"
	case strings.HasSuffix(lower, "sses"), strings.HasSuffix(lower, "shes"), strings.HasSuffix(lower, "ches"),
		strings.HasSuffix(lower, "xes"), strings.HasSuffix(lower, "zes"), strings.HasSuffix(lower, "uses"):
		return word[:len(word)-2]
	case strings.HasSuffix(lower, "ss"), strings.HasSuffix(lower, "us"), strings.HasSuffix(lower, "is"):
		return word
	case strings.HasSuffix(lower, "s") && len(word) > 1:
		return word[:len(word)-1]
	}
	return word
}

// typeDisplay is the display of the named type, or an empty one.
func (s schemaSet) typeDisplay(name string) ir.TypeDisplay {
	if typeDef := s.objectType(name); typeDef != nil && typeDef.Display != nil {
		return *typeDef.Display
	}
	return ir.TypeDisplay{}
}
