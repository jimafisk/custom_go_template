package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/dop251/goja"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
	"github.com/vanng822/css"
)

type Component struct {
	Name string
	Path string
}

// Render renders the template with the given data
func Render(path string, props map[string]any) (string, string, string, string) {

	c, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	template := string(c)

	// Split template into parts
	markup, fence, script, style := templateParts(template)
	// Get list of imported components and remove imports from fence
	fence, components := getComponents(path, fence)
	// Set the prop to the value that's passed in
	fence, fence_logic := setProps(fence, props)
	// Get list of all variables declared in fence
	allVars := getAllVars(fence)
	// Run the JS in Goja to get the computed values for props
	props = evaluateProps(fence, allVars, props)
	// Build AST with {if} and {for} controls + text nodes
	controlTree, err := buildControlTree(markup)
	if err != nil {
		fmt.Println(err)
	}
	markup, new_script, new_style := evalControlTree(controlTree, props, components)
	script += new_script
	style += new_style
	// Recursively render imported components
	//markup, script, style = renderComponents(markup, script, style, props, components)
	// Create scoped classes and add to html
	markup, scopedElements := scopeHTML(markup, props)
	// Add scoped classes to css
	style, _ = scopeCSS(style, scopedElements)

	ast, err := js.Parse(parse.NewInputString(script), js.Options{})
	if err != nil {
		panic(err)
	}
	script = ast.JSString()

	return markup, script, style, fence_logic
}

type scopedElement struct {
	tag         string
	id          string
	classes     []string
	scopedClass string
}

func scopeHTML(markup string, props map[string]any) (string, []scopedElement) {
	scopedElements := []scopedElement{}
	node, _ := html.Parse(strings.NewReader(markup))

	node, scopedElements = traverse(node, scopedElements, props)

	// Render the modified HTML back to a string
	buf := &strings.Builder{}
	err := html.Render(buf, node)
	if err != nil {
		log.Fatal(err)
	}
	markup = html.UnescapeString(buf.String())

	return markup, scopedElements
}

func scopeHTMLComp(comp_markup string, comp_props map[string]any, fence_logic string) (string, []scopedElement) {
	// We scope components differently than the full document
	// because html.Parse() builds a full document tree, aka wraps the component in <html><body></body></html>.
	// This shakes out when getting applied to the existing document tree, but we've scope styles for the html and body elements
	// To avoid scoped class conflicts, using html.ParseFragment() returns just the HTML for the component
	// Separating scopeHTML and scopeHTMLComp allows us to do both (avoid preemptively scoping html/body on comps, but do it on the doc entrypoint)
	// Related resources:
	// https://stackoverflow.com/questions/15081119/any-way-to-use-html-parse-without-it-adding-nodes-to-make-a-well-formed-tree
	// https://nikodoko.com/posts/html-table-parsing/
	scopedElements := []scopedElement{}
	fragments := []string{}
	nodes, _ := html.ParseFragment(strings.NewReader(comp_markup), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	for _, node := range nodes {
		node, scopedElements = traverse(node, scopedElements, comp_props)

		if len(comp_props) > 0 {
			x_data_str, x_init_str := makeGetter(comp_props, fence_logic)
			attr := html.Attribute{
				Key: "x-data",
				Val: x_data_str,
			}
			node.Attr = append(node.Attr, attr)
			attr = html.Attribute{
				Key: "x-init",
				Val: x_init_str,
			}
			node.Attr = append(node.Attr, attr)
		}

		buf := &strings.Builder{}
		err := html.Render(buf, node)
		if err != nil {
			log.Fatal(err)
		}
		fragments = append(fragments, html.UnescapeString(buf.String()))
	}
	comp_markup = ""
	for _, f := range fragments {
		comp_markup = comp_markup + f
	}

	return comp_markup, scopedElements
}

func traverse(node *html.Node, scopedElements []scopedElement, props map[string]any) (*html.Node, []scopedElement) {
	var traverse func(*html.Node)
	traverse = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "html" {
			if len(props) > 0 {
				attr := html.Attribute{
					Key: "x-data",
					Val: makeAttrStr(anyToString(props)),
				}
				node.Attr = append(node.Attr, attr)
			}
		}
		if node.Type == html.TextNode {
			if strings.Contains(node.Data, "{") && strings.Contains(node.Data, "}") {
				attr := html.Attribute{
					Key: "x-text",
					Val: "`" + strings.ReplaceAll(strings.ReplaceAll(node.Data, "{", "${"), "\"", "'") + "`",
				}
				node.Parent.Attr = append(node.Parent.Attr, attr)
			}
			node.Data = evalAllBrackets(node.Data, props)
		}
		if node.Type == html.ElementNode && node.DataAtom.String() != "" {
			tag := node.Data
			id := ""
			classes := []string{}
			scopedClass := getScopedClass(tag, "tag", scopedElements)

			if scopedClass == "" {
				// There wasn't an existing scoped class for the element, so create one
				randomStr, err := generateRandom()
				if err != nil {
					log.Fatal(err)
				}
				scopedClass = "plenti-" + randomStr
			}

			for i, attr := range node.Attr {
				if attr.Key == "id" {
					id = attr.Val
				}
				if attr.Key == "class" {
					classes = strings.Split(attr.Val, " ")
					alreadyScoped := false
					for _, class := range classes {
						if strings.HasPrefix(class, "plenti-") {
							alreadyScoped = true
							scopedClass = class
						}
					}
					if !alreadyScoped {
						node.Attr[i].Val += " " + scopedClass
					}
				}
				if strings.Contains(attr.Val, "{") && strings.Contains(attr.Val, "}") {
					if attr.Key != "x-text" && attr.Key != "x-data" && attr.Key != "x-init" && !strings.HasPrefix(attr.Key, ":") {
						node.Attr = append(node.Attr, html.Attribute{
							Key: ":" + attr.Key,
							Val: "`" + strings.ReplaceAll(strings.ReplaceAll(attr.Val, "{", "${"), "\"", "'") + "`",
						})
						node.Attr[i].Val = evalAllBrackets(attr.Val, props)
					}
				}
			}

			if len(classes) == 0 {
				node.Attr = append(node.Attr, html.Attribute{Key: "class", Val: scopedClass})
			}

			scopedElements = append(scopedElements, scopedElement{
				tag:         tag,
				id:          id,
				classes:     classes,
				scopedClass: scopedClass,
			})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			traverse(child)
		}
	}
	traverse(node)

	return node, scopedElements
}

type css_selectors struct {
	selectorStr string
	selectorArr []css_selector
}
type css_selector struct {
	tag     string
	classes []string
	id      string
}

func scopeCSS(style string, scopedElements []scopedElement) (string, []css_selectors) {
	ss := css.Parse(style)
	rules := ss.GetCSSRuleList()
	selectors := []css_selectors{}
	for rule_index, rule := range rules {
		tokens := rule.Style.Selector.Tokens
		selectorStr := rule.Style.Selector.Text()
		selectors = append(selectors, css_selectors{
			selectorStr: selectorStr,
			selectorArr: []css_selector{{}},
		})
		selector_index := 0
		for i, token := range tokens {
			if token.Type.String() == "S" && i+1 != len(tokens) {
				// Space indicates a nested selector
				selector_index++
				selectors[rule_index].selectorArr = append(selectors[rule_index].selectorArr, css_selector{})
			}
			if token.Type.String() == "IDENT" && (i < 1 || tokens[i-1].Value != ".") {
				tag := token.Value
				selectors[rule_index].selectorArr[selector_index].tag = tag
				for _, e := range scopedElements {
					if e.tag == tag && !strings.Contains(style, tag+".plenti-") {
						style = strings.ReplaceAll(style, tag, tag+"."+e.scopedClass)
						continue
					}
				}
			}
			if token.Type.String() == "CHAR" && token.Value == "." && i+1 < len(tokens) {
				class := tokens[i+1].Value
				selectors[rule_index].selectorArr[selector_index].classes = append(selectors[rule_index].selectorArr[selector_index].classes, class)
				for _, e := range scopedElements {
					for _, c := range e.classes {
						if c == class && !strings.Contains(style, class+".plenti-") && !strings.HasPrefix(class, "plenti-") {
							style = strings.ReplaceAll(style, class, class+"."+e.scopedClass)
							continue
						}
					}
				}
			}
			if token.Type.String() == "HASH" {
				id := strings.TrimPrefix(token.Value, "#")
				for _, e := range scopedElements {
					if e.id == id && !strings.Contains(style, id+".plenti-") {
						style = strings.ReplaceAll(style, id, id+"."+e.scopedClass)
						continue
					}
				}
				selectors[rule_index].selectorArr[selector_index].id = id
			}
		}
	}

	// The `selectors` var isn't currently used, but could be useful for removing unused styles
	// or only setting classes in HTML if the selector exists in CSS
	return style, selectors

}

type visitor struct {
	scopedElements []scopedElement
}

func (*visitor) Exit(js.INode) {}

func (v *visitor) Enter(node js.INode) js.IVisitor {
	switch node := node.(type) {
	case *js.Var:
		if node.Decl.String() == "LexicalDecl" && !strings.Contains(node.String(), "_plenti_") {
			randomStr, _ := generateRandom()
			node.Data = append(node.Data, []byte("_plenti_"+randomStr)...)
		}
	case *js.BindingElement:
		if expr := node.Default; expr != nil {
			if callExpr, ok := expr.(*js.CallExpr); ok {
				// Check if it's a member expression (like document.querySelector)
				if memberExpr, ok := callExpr.X.(*js.DotExpr); ok {
					objName := string(memberExpr.X.String())
					propName := string(memberExpr.Y.Data)
					if objName == "document" && propName == "querySelector" {
						for i, arg := range callExpr.Args.List {
							argStrOrig := strings.Trim(arg.String(), "\"")
							argStr := argStrOrig
							target_type := "tag"
							if strings.HasPrefix(argStr, ".") {
								argStr = strings.TrimPrefix(argStr, ".")
								target_type = "class"
							}
							if strings.HasPrefix(argStr, "#") {
								argStr = strings.TrimPrefix(argStr, "#")
								target_type = "id"
							}
							scopedClass := getScopedClass(argStr, target_type, v.scopedElements)
							newData := []byte(`"` + argStrOrig + `"`)
							if !strings.Contains(argStrOrig, "plenti-") {
								newData = []byte(`"` + argStrOrig + "." + scopedClass + `"`)
							}
							callExpr.Args.List[i] = js.Arg{Value: &js.LiteralExpr{
								Data: newData,
							}}
						}
					}
				}
			}
		}
		//fmt.Println(node)
	case *js.Element:
		//fmt.Println(node.Value.String())
	default:
		//fmt.Println()
		//fmt.Println(node.String())
	}
	return v
}

func scopeJS(script string, scopedElements []scopedElement) string {
	ast, _ := js.Parse(parse.NewInputString(script), js.Options{})
	v := visitor{scopedElements: scopedElements}
	js.Walk(&v, ast)
	script = ast.JSString()
	return script
}

func getScopedClass(target string, target_type string, scopedElements []scopedElement) string {
	for _, elem := range scopedElements {
		if target_type == "tag" && elem.tag == target {
			return elem.scopedClass
		}
		if target_type == "id" && elem.id == target {
			return elem.scopedClass
		}
		if target_type == "class" {
			for _, class := range elem.classes {
				if class == target {
					return elem.scopedClass
				}
			}
		}
	}
	return ""
}

func generateRandom() (string, error) {
	chars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var bytes = make([]byte, 6)
	for i := range bytes {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		bytes[i] = chars[num.Int64()]
	}
	return string(bytes), nil
}

func templateParts(template string) (string, string, string, string) {
	reFence := regexp.MustCompile(`(?s)---(.*?)---`)
	reScript := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	reStyle := regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	fences := reFence.FindAllStringSubmatch(template, -1)
	scripts := reScript.FindAllStringSubmatch(template, -1)
	styles := reStyle.FindAllStringSubmatch(template, -1)
	if len(fences) > 1 {
		log.Fatal("Can only have one set of Fences (--- and ---) per template")
	}
	if len(scripts) > 1 {
		log.Fatal("Can only have one set of Script tags (<script></script>) per template")
	}
	if len(styles) > 1 {
		log.Fatal("Can only have one set of Style tags (<style></style>) per template")
	}
	markup := template
	fence := ""
	script := ""
	style := ""
	if len(fences) > 0 {
		wrapped_fence := fences[0][0]
		fence = fences[0][1]
		markup = strings.Replace(markup, wrapped_fence, "", 1)
	}
	if len(scripts) > 0 {
		wrapped_script := scripts[0][0]
		script = scripts[0][1]
		markup = strings.Replace(markup, wrapped_script, "", 1)
	}
	if len(styles) > 0 {
		wrapped_style := styles[0][0]
		style = styles[0][1]
		markup = strings.Replace(markup, wrapped_style, "", 1)
	}
	return markup, fence, script, style
}

func setProps(fence string, props map[string]any) (string, string) {
	fence_logic := fence
	for name, value := range props {
		reProp := regexp.MustCompile(fmt.Sprintf(`prop (%s)(\s?=\s?(.*?))?;`, name))
		fence_logic = reProp.ReplaceAllString(fence_logic, "")
		fence = reProp.ReplaceAllString(fence, "let "+name+" = "+anyToString(value)+";")
	}
	// Convert prop to let for unpassed props
	rePropDefaults := regexp.MustCompile(`prop (.*?);`)
	fence_logic = rePropDefaults.ReplaceAllString(fence_logic, "")
	fence = rePropDefaults.ReplaceAllString(fence, "let $1;") // Works with equals or not

	fence_logic = makeAttrStr(fence_logic)

	return fence, fence_logic
}

func makeAttrStr(str string) string {
	reComments := regexp.MustCompile(`//.*`)
	str = reComments.ReplaceAllString(str, "") // Remove comments before putting on single line

	str = strings.TrimSpace(str)              // Remove leading and trailing whitespace
	str = strings.ReplaceAll(str, "\n", "")   // Remove all tabs to put on single line
	str = strings.ReplaceAll(str, "'", "\\'") // escape single quotes
	str = strings.ReplaceAll(str, "\"", "'")  // change double quotes to single

	return str
}

func getAllVars(fence string) []string {
	allVars := []string{}
	reAllVars := regexp.MustCompile(`(?:let|const|var) (?P<name>.*?)(?:\s?=\s?(?P<value>.*?))?;`)
	nameIndex := reAllVars.SubexpIndex("name")
	//valueIndex := reAllVars.SubexpIndex("value")
	matches := reAllVars.FindAllStringSubmatch(fence, -1)
	for _, currentVar := range matches {
		// Don't need to set value since that gets evaluated in Goja
		allVars = append(allVars, currentVar[nameIndex])
	}
	return allVars
}

func evaluateProps(fence string, allVars []string, props map[string]any) map[string]any {
	vm := goja.New()
	vm.RunString(fence)
	for _, name := range allVars {
		evaluated_value := vm.Get(name).Export()
		if evaluated_value == nil {
			evaluated_value = ""
		}
		props[name] = evaluated_value
	}
	return props
}

func evalAllBrackets(str string, props map[string]any) string {
	for {
		startPos := strings.IndexRune(str, '{')
		endPos := strings.IndexRune(str, '}')
		if startPos == -1 || endPos == -1 {
			break
		}
		jsCode := str[startPos+1 : endPos]
		evaluated := fmt.Sprintf("%v", evalJS(jsCode, props)) // Like anyToString but doesn't wrap strings in quotes
		str = str[0:startPos] + evaluated + str[endPos+1:]
	}
	return str
}

func declProps(props map[string]any) string {
	props_decl := ""
	for prop_name, prop_value := range props {
		props_decl += "let " + prop_name + " = " + anyToString(prop_value) + ";"
	}
	return props_decl
}

func evalJS(jsCode string, props map[string]any) any {
	props_decl := declProps(props)
	vm := goja.New()
	goja_value, err := vm.RunString(props_decl + jsCode)
	if err != nil {
		return ""
		//return jsCode
	}
	return goja_value.Export()
}

// Helper function to check if a character is uppercase
func isUpper(c byte) bool {
	return c >= 'A' && c <= 'Z'
}

type control struct {
	isIfStmt    bool
	ifCondition string

	isElseIfStmt    bool
	elseIfCondition string

	isElseStmt bool

	isForLoop     bool
	forVar        string
	forCollection string

	isTextNode  bool
	textContent string

	isComp    bool
	compName  string
	compProps map[string]any

	isDynamicComp    bool
	dynamicCompPath  string
	dynamicCompProps map[string]any

	children []control
}

func buildControlTree(markup string) ([]control, error) {
	var controlTree []control
	var controlStack []*control
	var openControl *control
	for i := 0; i < len(markup); {
		if strings.HasPrefix(markup[i:], "{if ") {
			startOpenIfIndex := i

			relativeEndOpenIfIndex := strings.Index(markup[startOpenIfIndex:], "}")
			if relativeEndOpenIfIndex == -1 {
				return nil, fmt.Errorf("{if ...} condition missing closing \"}\" at index %d", startOpenIfIndex)
			}
			endOpenIfIndex := startOpenIfIndex + relativeEndOpenIfIndex

			ifCondition := markup[startOpenIfIndex+len("{if ") : endOpenIfIndex]

			newControl := control{
				isIfStmt:    true,
				ifCondition: ifCondition,
			}

			if openControl != nil {
				openControl.children = append(openControl.children, newControl)
				controlStack = append(controlStack, &openControl.children[len(openControl.children)-1])
			} else {
				controlTree = append(controlTree, newControl)
				controlStack = append(controlStack, &controlTree[len(controlTree)-1])
			}
			openControl = controlStack[len(controlStack)-1]

			i = endOpenIfIndex + 1
		} else if strings.HasPrefix(markup[i:], "{for ") {
			startOpenForIndex := i
			relativeEndOpenForIndex := strings.Index(markup[startOpenForIndex:], "}")
			if relativeEndOpenForIndex == -1 {
				return nil, fmt.Errorf("{for } loop missing closing \"}\" at index %d", startOpenForIndex)
			}
			endOpenForIndex := startOpenForIndex + relativeEndOpenForIndex

			re := regexp.MustCompile(`for (?:let|var|const) (\w+) (?:of|in) (.*)`)
			matches := re.FindStringSubmatch(markup[startOpenForIndex:endOpenForIndex])
			if len(matches) < 2 {
				return nil, fmt.Errorf("{for } loop missing iterator / collection \"}\" at index %d", startOpenForIndex)
			}

			newControl := control{
				isForLoop:     true,
				forVar:        matches[1],
				forCollection: matches[2],
			}
			if openControl != nil {
				openControl.children = append(openControl.children, newControl)
				controlStack = append(controlStack, &openControl.children[len(openControl.children)-1])
			} else {
				controlTree = append(controlTree, newControl)
				controlStack = append(controlStack, &controlTree[len(controlTree)-1])
			}
			openControl = controlStack[len(controlStack)-1]

			i = endOpenForIndex + 1
		} else if strings.HasPrefix(markup[i:], "{else if ") {
			if openControl == nil {
				return nil, fmt.Errorf("{else if} at index %d missing opening {if}", i)
			}
			startElseIfIndex := i

			relativeEndElseIfIndex := strings.Index(markup[startElseIfIndex:], "}")
			if relativeEndElseIfIndex == -1 {
				return nil, fmt.Errorf("{else if} condition missing closing \"}\" at index %d", startElseIfIndex)
			}
			endElseIfIndex := startElseIfIndex + relativeEndElseIfIndex

			elseIfCondition := markup[startElseIfIndex+len("{else if ") : endElseIfIndex]

			if openControl.isElseIfStmt {
				controlStack = controlStack[:len(controlStack)-1] // Pop from stack
				openControl = controlStack[len(controlStack)-1]
			}

			openControl.children = append(openControl.children, control{
				isElseIfStmt:    true,
				elseIfCondition: elseIfCondition,
			})
			controlStack = append(controlStack, &openControl.children[len(openControl.children)-1])
			openControl = controlStack[len(controlStack)-1]

			i = endElseIfIndex + 1
		} else if strings.HasPrefix(markup[i:], "{else}") {
			if openControl == nil {
				return nil, fmt.Errorf("{else} at index %d missing opening {if}", i)
			}
			newControl := control{
				isElseStmt: true,
			}

			if openControl.isElseIfStmt {
				controlStack = controlStack[:len(controlStack)-1] // Pop from stack
				openControl = controlStack[len(controlStack)-1]
			}
			openControl.children = append(openControl.children, newControl)
			controlStack = append(controlStack, &openControl.children[len(openControl.children)-1])
			openControl = controlStack[len(controlStack)-1]

			i += len("{else}")
		} else if i+1 < len(markup) && markup[i] == '<' && isUpper(markup[i+1]) {
			startCompIndex := i
			relativeEndCompIndex := strings.Index(markup[startCompIndex:], "/>")
			if relativeEndCompIndex == -1 {
				return nil, fmt.Errorf("Component missing closing \"/>\" at index %d", startCompIndex)
			}
			endCompIndex := startCompIndex + relativeEndCompIndex

			startCompNameIndex := i + 1
			relativeEndCompNameIndex := strings.Index(markup[startCompNameIndex:], " ")
			endCompNameIndex := startCompNameIndex + relativeEndCompNameIndex

			compName := markup[startCompNameIndex:endCompNameIndex]
			compProps := markup[endCompNameIndex+1 : endCompIndex]

			newControl := control{
				isComp:    true,
				compName:  compName,
				compProps: getCompArgs(compProps),
			}

			// TODO: For now Comp won't have children (eventually add slot support)
			if openControl != nil {
				openControl.children = append(openControl.children, newControl)
			} else {
				controlTree = append(controlTree, newControl)
			}

			i = endCompIndex + len("/>")
		} else if strings.HasPrefix(markup[i:], "<=") {
			startDynamicCompIndex := i
			relativeEndDynamicCompIndex := strings.Index(markup[startDynamicCompIndex:], "/>")
			if relativeEndDynamicCompIndex == -1 {
				return nil, fmt.Errorf("<= dynamic comp missing closing \"/>\" at index %d", startDynamicCompIndex)
			}
			endDynamicCompIndex := startDynamicCompIndex + relativeEndDynamicCompIndex

			startDynamicCompPathIndex := startDynamicCompIndex + len("<='")
			// TODO: dynamic paths now need to be wrapped in either single or double quotes
			relativeEndDynamicCompPathIndex := strings.IndexAny(markup[startDynamicCompPathIndex:], "'\"")
			endDynamicCompPathIndex := startDynamicCompPathIndex + relativeEndDynamicCompPathIndex
			dynamicCompPath := markup[startDynamicCompPathIndex:endDynamicCompPathIndex]
			dynamicCompProps := markup[endDynamicCompPathIndex+1 : endDynamicCompIndex]
			/*
				reDynamicComponent := regexp.MustCompile(`<=(".*?"|'.*?'|{.*?})\s(.*?)?(?:\s)?/>`)
				matches := reDynamicComponent.FindAllStringSubmatch(markup, -1)
				var comp_path string
				var comp_props map[string]any
				for _, match := range matches {
					if len(match) > 0 {
						comp_path = strings.Trim(match[1], "\"")
					}
					if len(match) > 1 {
						comp_props = getCompArgs(match[2])
					}
				}
			*/

			newControl := control{
				isDynamicComp:    true,
				dynamicCompPath:  strings.Trim(dynamicCompPath, "'\""),
				dynamicCompProps: getCompArgs(dynamicCompProps),
			}

			// TODO: For now dynamicComp won't have children (eventually add slot support)
			if openControl != nil {
				openControl.children = append(openControl.children, newControl)
			} else {
				controlTree = append(controlTree, newControl)
			}

			i = endDynamicCompIndex + len("/>")
		} else if strings.HasPrefix(markup[i:], "{/if}") {
			if openControl == nil {
				return nil, fmt.Errorf("closing {/if} at index %d without opening {if}", i)
			}
			if openControl.isElseIfStmt || openControl.isElseStmt {
				controlStack = controlStack[:len(controlStack)-1] // Pop from stack
			}
			controlStack = controlStack[:len(controlStack)-1] // Pop from stack
			if len(controlStack) > 0 {
				openControl = controlStack[len(controlStack)-1]
			} else {
				openControl = nil
			}
			i += len("{/if}")
		} else if strings.HasPrefix(markup[i:], "{/for}") {
			if openControl == nil {
				return nil, fmt.Errorf("closing {/for} at index %d without opening {for}", i)
			}
			controlStack = controlStack[:len(controlStack)-1] // Pop from stack
			if len(controlStack) > 0 {
				openControl = controlStack[len(controlStack)-1]
			} else {
				openControl = nil
			}
			i += len("{/for}")
		} else {
			start := i
			for i < len(markup) &&
				!strings.HasPrefix(markup[i:], "<=") &&
				!(i+1 < len(markup) && markup[i] == '<' && isUpper(markup[i+1])) &&
				!strings.HasPrefix(markup[i:], "{if ") &&
				!strings.HasPrefix(markup[i:], "{for ") &&
				!strings.HasPrefix(markup[i:], "{else if ") &&
				!strings.HasPrefix(markup[i:], "{else}") &&
				!strings.HasPrefix(markup[i:], "{/if}") &&
				!strings.HasPrefix(markup[i:], "{/for}") {
				i++
			}
			if start < i {
				newControl := control{
					isTextNode:  true,
					textContent: markup[start:i],
				}
				if openControl != nil {
					openControl.children = append(openControl.children, newControl)
					// Note: Not adding text nodes to controlStack as they don't need closing
				} else {
					controlTree = append(controlTree, newControl)
					// Note: Not adding text nodes to controlStack as they don't need closing
				}
			}
		}
	}

	return controlTree, nil
}

func evalControlTree(controlTree []control, props map[string]any, components []Component) (string, string, string) {
	var markupBuilder strings.Builder
	var scriptBuilder strings.Builder
	var styleBuilder strings.Builder

	for _, ctrl := range controlTree {
		if ctrl.isTextNode {
			markupBuilder.WriteString(evalAllBrackets(ctrl.textContent, props))
		} else if ctrl.isIfStmt {
			if isBoolAndTrue(evalJS(ctrl.ifCondition, props)) {
				markup, _, _ := evalControlTree(ctrl.children, props, components)
				markupBuilder.WriteString(markup)
			} else {
				evaluated := false
				// Process else-if statements
				for _, child := range ctrl.children {
					if child.isElseIfStmt && isBoolAndTrue(evalJS(child.elseIfCondition, props)) {
						markup, _, _ := evalControlTree(child.children, props, components)
						markupBuilder.WriteString(markup)
						evaluated = true
						break
					}
				}
				// Process else statement if no else-if was true
				if !evaluated {
					for _, child := range ctrl.children {
						if child.isElseStmt {
							markup, _, _ := evalControlTree(child.children, props, components)
							markupBuilder.WriteString(markup)
							break
						}
					}
				}
			}
		} else if ctrl.isForLoop {
			iterableVal := evalJS(ctrl.forCollection, props)
			items, ok := iterableVal.([]any)
			if ok {
				for _, item := range items {
					newProps := make(map[string]any)
					for k, v := range props {
						newProps[k] = v
					}
					newProps[ctrl.forVar] = item
					markup, _, _ := evalControlTree(ctrl.children, newProps, components)
					markupBuilder.WriteString(markup)
				}
			}
		} else if ctrl.isComp {
			newProps := make(map[string]any)
			for prop_name, prop_value := range ctrl.compProps {
				// Evaluate the passed in props within the context of the parent comp
				newProps[prop_name] = evalJS(fmt.Sprintf(`%s`, prop_value), props)
			}
			var compPath string
			for _, comp := range components {
				if comp.Name == ctrl.compName {
					compPath = comp.Path
				}
			}
			markup, script, style, fence_logic := Render(compPath, newProps)
			// Create scoped classes and add to html
			markup, scopedElements := scopeHTMLComp(markup, props, fence_logic)
			// Add scoped classes to css
			style, _ = scopeCSS(style, scopedElements)
			markupBuilder.WriteString(markup)
			scriptBuilder.WriteString(script)
			styleBuilder.WriteString(style)
		} else if ctrl.isDynamicComp {
			newProps := make(map[string]any)
			for prop_name, prop_value := range ctrl.dynamicCompProps {
				// Evaluate the passed in props within the context of the parent comp
				newProps[prop_name] = evalJS(fmt.Sprintf(`%s`, prop_value), props)
			}
			evaluatedCompPath := evalAllBrackets(ctrl.dynamicCompPath, props)
			markup, script, style, fence_logic := Render(evaluatedCompPath, newProps)
			// Create scoped classes and add to html
			markup, scopedElements := scopeHTMLComp(markup, props, fence_logic)
			// Add scoped classes to css
			style, _ = scopeCSS(style, scopedElements)
			markupBuilder.WriteString(markup)
			scriptBuilder.WriteString(script)
			styleBuilder.WriteString(style)
		}
	}

	return markupBuilder.String(), scriptBuilder.String(), styleBuilder.String()
}

func getComponents(path, fence string) (string, []Component) {
	parentCompDir := filepath.Dir(path)
	components := []Component{}
	reImport := regexp.MustCompile(`import\s+([A-Za-z_][A-Za-z_0-9]*)\s+from\s*"([^"]+)";`)
	for _, line := range strings.Split(fence, "\n") {
		match := reImport.FindStringSubmatch(line)
		if len(match) > 1 {
			compName := match[1]
			compPath := match[2]
			if filepath.IsAbs(compPath) {
				compPath = "." + filepath.Clean("/"+compPath)
			} else {
				compPath = filepath.Join(parentCompDir, filepath.Clean("/"+compPath))
			}
			components = append(components, Component{
				Name: compName,
				Path: compPath,
			})
			fence = reImport.ReplaceAllString(fence, "") // Remove current import so script can run in goja
		}
	}
	return fence, components
}

func getCompArgs(comp_decl string) map[string]any {
	comp_args := strings.SplitAfter(comp_decl, "}")
	comp_props := map[string]any{}
	for _, comp_arg := range comp_args {
		comp_arg = strings.TrimSpace(comp_arg)
		if strings.HasPrefix(comp_arg, "{") && strings.HasSuffix(comp_arg, "}") {
			prop_name := strings.Trim(comp_arg, "{}")
			comp_props[prop_name] = prop_name
		}
		if strings.Contains(comp_arg, "={") && strings.HasSuffix(comp_arg, "}") {
			nameEndPos := strings.IndexRune(comp_arg, '=')
			prop_name := comp_arg[0:nameEndPos]

			valueStartPos := strings.IndexRune(comp_arg, '{')
			valueEndPos := strings.IndexRune(comp_arg, '}')

			comp_props[prop_name] = comp_arg[valueStartPos+1 : valueEndPos]
		}
	}
	return comp_props
}

func renderComponents(markup, script, style string, props map[string]any, components []Component) (string, string, string) {
	// Handle staticly imported components
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s\s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(markup, -1)
		for _, match := range matches {
			if len(match) > 1 {
				comp_props := getCompArgs(match[1])
				// Recursively render imports
				comp_markup, comp_script, comp_style, fence_logic := Render(component.Path, comp_props)
				// Create scoped classes and add to html
				comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup, comp_props, fence_logic)
				// Add scoped classes to css
				comp_style, _ = scopeCSS(comp_style, comp_scopedElements)
				// Add scoped classes to js
				comp_script = scopeJS(comp_script, comp_scopedElements)

				// Replace only one component (in case multiple of the same comps are placed on the page)
				found := reComponent.FindString(markup)
				if found != "" {
					markup = strings.Replace(markup, found, comp_markup, 1)
				}
				script = script + comp_script
				style = style + comp_style
			}
		}
	}
	// Handle dynamic components
	reDynamicComponent := regexp.MustCompile(`<=(".*?"|'.*?'|{.*?})\s(.*?)?(?:\s)?/>`)
	matches := reDynamicComponent.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		if len(match) >= 1 {
			comp_path := match[1]
			if strings.Contains(comp_path, `{`) && strings.Contains(comp_path, `}`) {
				comp_path = evalAllBrackets(comp_path, props)
			}
			comp_props := getCompArgs(match[2])
			comp_path = strings.Trim(comp_path, "\"'`") // Remove backticks, single and double quotes
			comp_markup, comp_script, comp_style, fence_logic := Render(comp_path, comp_props)
			// Create scoped classes and add to html
			comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup, comp_props, fence_logic)
			// Add scoped classes to css
			comp_style, _ = scopeCSS(comp_style, comp_scopedElements)

			// Replace one string
			found := reDynamicComponent.FindString(markup)
			if found != "" {
				markup = strings.Replace(markup, found, comp_markup, 1)
			}
			script = script + comp_script
			style = style + comp_style
		}
	}

	return markup, script, style
}

func formatArray(value any) string {
	val := reflect.ValueOf(value)
	var elements []string
	for i := 0; i < val.Len(); i++ {
		elem := val.Index(i).Interface()
		elements = append(elements, anyToString(elem)) // Recursively format each element
	}
	return "[" + strings.Join(elements, ", ") + "]"
}

func formatObject(value any) string {
	val := reflect.ValueOf(value)
	if val.Kind() != reflect.Map {
		return ""
	}

	// Get the map keys
	keys := val.MapKeys()

	// Convert keys to a slice of interfaces
	keyInterfaces := make([]interface{}, len(keys))
	for i, key := range keys {
		keyInterfaces[i] = key.Interface()
	}

	// Sort the keys (assuming they are strings)
	sort.Slice(keyInterfaces, func(i, j int) bool {
		return fmt.Sprintf("%v", keyInterfaces[i]) < fmt.Sprintf("%v", keyInterfaces[j])
	})

	// Format the map entries
	var pairs []string
	for _, key := range keyInterfaces {
		value := val.MapIndex(reflect.ValueOf(key))
		pairs = append(pairs, fmt.Sprintf("%v: %v", key, anyToString(value.Interface())))
	}

	return "{" + strings.Join(pairs, ", ") + "}"
}

func formatElement(value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return "unknown type"
	}
}

func anyToString(value any) string {
	val := reflect.ValueOf(value)
	switch val.Kind() {
	case reflect.Array, reflect.Slice:
		return formatArray(value)
	case reflect.Map:
		return formatObject(value)
	default:
		return formatElement(value)
	}
}

func makeGetter(comp_data map[string]any, fence_logic string) (string, string) {
	x_data_str := fmt.Sprintf("_fence: `%s`,", fence_logic)

	params := make([]string, 0, len(comp_data))
	args := make([]string, 0, len(comp_data))
	for k, v := range comp_data {
		params = append(params, k)

		value_str := fmt.Sprintf("%s", v) // Any to string
		value_str = makeAttrStr(value_str)

		for prop_name, _ := range comp_data {
			if strings.Contains(value_str, prop_name) {
				// TODO: This string replacement is sloppy and could target partial variables or string values
				value_str = strings.ReplaceAll(value_str, prop_name, "Alpine.$data($el.parentElement)."+prop_name)
			}

		}
		args = append(args, value_str)
	}
	x_data_str += strings.Join(append(params, ""), ": undefined, ")
	params_str := strings.Join(params, ", ")
	args_str := strings.Join(args, ", ")

	i := 0
	var x_init_str string
	for name := range comp_data {
		//x_data_str += fmt.Sprintf("get %s() {return (new Function('%s', `${this._fence}; return %s;`))(%s); },", name, params_str, name, args_str)
		x_init_str += fmt.Sprintf("%s = new Function('%s', `${_fence}; return %s;`)(%s),", name, params_str, name, args_str)
		x_init_str += fmt.Sprintf("$watch('Alpine.$data($el.parentElement)', () => %s = new Function('%s', `${_fence}; return %s;`)(%s)),", name, params_str, name, args_str)
		i++
	}
	return "{" + x_data_str + "}", strings.TrimRight(x_init_str, ",")
}

func isBoolAndTrue(value any) bool {
	if b, ok := value.(bool); ok && b {
		return true
	}
	return false
}

func copyFile(sourcePath, destPath string) {
	// Open the source file
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		panic(err)
	}
	defer sourceFile.Close()

	// Create the destination file
	destinationFile, err := os.Create(destPath)
	if err != nil {
		panic(err)
	}
	defer destinationFile.Close()

	// Copy the contents from the source file to the destination file
	_, err = io.Copy(destinationFile, sourceFile)
	if err != nil {
		panic(err)
	}
}

func main() {
	// Render the template with data
	props := map[string]any{"name": "J", "age": 2, "animals": []string{"cat", "dog", "pig"}}
	markup, script, style, _ := Render("views/home.html", props)
	os.WriteFile("./public/script.js", []byte(script), fs.ModePerm)
	os.WriteFile("./public/style.css", []byte(style), fs.ModePerm)
	os.WriteFile("./public/index.html", []byte(markup), fs.ModePerm)
	copyFile("./views/cms.js", "./public/cms.js")
	copyFile("./views/cms.css", "./public/cms.css")

	http.Handle("/", http.FileServer(http.Dir("./public")))
	fmt.Println("visit site at: http://localhost:3000")
	http.ListenAndServe(":3000", nil)
}
