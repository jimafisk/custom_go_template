package main

import (
	"crypto/rand"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"reflect"
	"regexp"
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
func Render(path string, props map[string]any) (string, string, string) {

	c, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	template := string(c)

	// Split template into parts
	markup, fence, script, style := templateParts(template)
	// Get list of imported components and remove imports from fence
	fence, components := getComponents(fence)
	// Set the prop to the value that's passed in
	fence = setProps(fence, props)
	// Get list of all variables declared in fence
	allVars := getAllVars(fence)
	// Run the JS in Goja to get the computed values for props
	props = evaluateProps(fence, allVars, props)
	// Run template conditions {if}{else}{/if}
	//markup = renderConditions(markup, props)
	markup, _ = FindIfConditions(markup, props)
	// Run template loops {for let _ in _}{/for} and {for let _ of _}{/for}
	markup = renderLoops(markup, props)
	// Recursively render imported components
	markup, script, style = renderComponents(markup, script, style, props, components)
	// Create scoped classes and add to html
	markup, scopedElements := scopeHTML(markup, props)
	// Add scoped classes to css
	style, _ = scopeCSS(style, scopedElements)

	ast, err := js.Parse(parse.NewInputString(script), js.Options{})
	if err != nil {
		panic(err)
	}
	script = ast.JSString()

	return markup, script, style
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

func scopeHTMLComp(comp_markup string, comp_props map[string]any, comp_data map[string]any) (string, []scopedElement) {
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
			attr := html.Attribute{
				Key: "x-data",
				Val: makeGetter(comp_data),
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
					Val: strings.ReplaceAll(anyToString(props), "\"", "'"),
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
					if attr.Key != "x-text" && attr.Key != "x-data" && !strings.HasPrefix(attr.Key, ":") {
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

func setProps(fence string, props map[string]any) string {
	for name, value := range props {
		reProp := regexp.MustCompile(fmt.Sprintf(`prop (%s)(\s?=\s?(.*?))?;`, name))
		fence = reProp.ReplaceAllString(fence, "let "+name+" = "+anyToString(value)+";")
	}
	// Convert prop to let for unpassed props
	rePropDefaults := regexp.MustCompile(`prop (.*?);`)
	fence = rePropDefaults.ReplaceAllString(fence, "let $1;") // Works with equals or not

	return fence
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

type conditional struct {
	ifCondition      string
	elseIfConditions []string

	startOpenIfIndex   int
	startElseIfIndexes []int
	startElseIndex     int

	startIfContentIndex       int
	startElseIfContentIndexes []int
	startElseContentIndex     int
}

// FindIfConditions finds if-conditions in a given string
func FindIfConditions(markup string, props map[string]any) (string, error) {
	var conditionals []conditional
	var currentConditional conditional
	for i := 0; i < len(markup); i++ {
		if strings.HasPrefix(markup[i:], "{if ") {
			startOpenIfIndex := i

			relativeEndOpenIfIndex := strings.Index(markup[startOpenIfIndex:], "}")
			if relativeEndOpenIfIndex == -1 {
				return "", fmt.Errorf("unbalanced if-condition at index %d", startOpenIfIndex)
			}
			endOpenIfIndex := startOpenIfIndex + relativeEndOpenIfIndex

			ifCondition := markup[startOpenIfIndex+len("{if ") : endOpenIfIndex]

			startIfContentIndex := endOpenIfIndex + 1
			currentConditional = conditional{
				startOpenIfIndex:    startOpenIfIndex,
				ifCondition:         ifCondition,
				startIfContentIndex: startIfContentIndex,
			}
			conditionals = append(conditionals, currentConditional)
		} else if strings.HasPrefix(markup[i:], "{else if ") {
			startElseIfIndex := i
			currentConditional.startElseIfIndexes = append(currentConditional.startElseIfIndexes, startElseIfIndex)

			relativeEndElseIfIndex := strings.Index(markup[startElseIfIndex:], "}")
			if relativeEndElseIfIndex == -1 {
				return "", fmt.Errorf("unbalanced else-if-condition at index %d", startElseIfIndex)
			}
			endElseIfIndex := startElseIfIndex + relativeEndElseIfIndex

			elseIfCondition := markup[startElseIfIndex+len("{else if ") : endElseIfIndex]
			currentConditional.elseIfConditions = append(currentConditional.elseIfConditions, elseIfCondition)

			startElseIfContentIndex := endElseIfIndex + 1
			currentConditional.startElseIfContentIndexes = append(currentConditional.startElseIfContentIndexes, startElseIfContentIndex)
		} else if strings.HasPrefix(markup[i:], "{else}") {
			startElseIndex := i
			currentConditional.startElseIndex = startElseIndex
			endElseIndex := startElseIndex + len("{else}")

			startElseContentIndex := endElseIndex + 1
			currentConditional.startElseContentIndex = startElseContentIndex
		} else if strings.HasPrefix(markup[i:], "{/if}") {
			startCloseIfIndex := i
			endIfContentIndex := startCloseIfIndex

			startOpenIfIndex := currentConditional.startOpenIfIndex
			ifCondition := currentConditional.ifCondition

			if isBoolAndTrue(evalJS(ifCondition, props)) {
				startIfContentIndex := currentConditional.startIfContentIndex
				if len(currentConditional.startElseIfIndexes) > 0 {
					endIfContentIndex = currentConditional.startElseIfIndexes[0] - 1
				} else if currentConditional.startElseIndex > 0 {
					// Although 0 is a valid index, an {else} should never be in first position, so this is a valid way to check if "else" was set
					endIfContentIndex = currentConditional.startElseIndex - 1
				}

				ifContent := markup[startIfContentIndex:endIfContentIndex]
				modifiedMarkup := markup[:startOpenIfIndex] + ifContent + markup[startCloseIfIndex+len("{/if}"):]
				i -= len(markup) - len(modifiedMarkup) // Move crawler back by amount of shrinkage
				markup = modifiedMarkup
			} else {
				elseIfWasTrue := false
				endElseIfContentIndex := endIfContentIndex
				numOfConditions := len(currentConditional.elseIfConditions)
				for condPos, elseIfCondition := range currentConditional.elseIfConditions {
					if isBoolAndTrue(evalJS(elseIfCondition, props)) && !elseIfWasTrue {
						elseIfWasTrue = true
						startElseIfContentIndex := currentConditional.startElseIfContentIndexes[condPos]
						if numOfConditions > condPos {
							// If there are more else if conditions, the start of the next one is the end of the current one
							endElseIfContentIndex = currentConditional.startElseIfIndexes[condPos+1]
						}
						if condPos == numOfConditions && currentConditional.startElseIndex > 0 {
							// Last if else statement is true and there's an else after
							endElseIfContentIndex = currentConditional.startElseIndex
						}
						currentElseIfContent := markup[startElseIfContentIndex:endElseIfContentIndex]
						modifiedMarkup := markup[:startOpenIfIndex] + currentElseIfContent + markup[startCloseIfIndex+len("{/if}"):]
						i -= len(markup) - len(modifiedMarkup)
						markup = modifiedMarkup
					}
				}
				if !elseIfWasTrue && currentConditional.startElseIndex > 0 {
					startElseContentIndex := currentConditional.startElseContentIndex
					currentElseContent := markup[startElseContentIndex:startCloseIfIndex]
					modifiedMarkup := markup[:startOpenIfIndex] + currentElseContent + markup[startCloseIfIndex+len("{/if}"):]
					i -= len(markup) - len(modifiedMarkup)
					markup = modifiedMarkup
				}
			}
			// Remove current conditional from stack
			conditionals = conditionals[:len(conditionals)-1]
			if len(conditionals) > 0 {
				// Point to the new last item in the list
				currentConditional = conditionals[len(conditionals)-1]
			}
		}
	}

	return markup, nil
}

func renderConditions(markup string, props map[string]any) string {
	reCondition := regexp.MustCompile(`(?s){(if)\s(.*?)}(.*?)(?:{(?:(else\sif)\s(.*?)}(.*?)|(?:(else))}(.*?))){0,}{/if}`)
	matches := reCondition.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		full_match := match[0]
		for i, part := range match {
			if part == "if" || part == "else if" {
				condition := match[i+1]
				result := match[i+2]
				nestedIfIndex := strings.Index(result, "{if")
				if nestedIfIndex >= 0 {
					full_match = full_match[nestedIfIndex:]
					markup = strings.Replace(markup, full_match, result, 1)
				}
				if isBoolAndTrue(evalJS(condition, props)) {
					markup = strings.Replace(markup, full_match, result, 1)
					break
				}
			}
			if part == "else" {
				result := match[i+1]
				markup = strings.Replace(markup, full_match, result, 1)
				break
			}
		}
		markup = strings.Replace(markup, full_match, "", 1) // Did not match any conditions, just remove it
	}
	return markup
}

func renderLoops(markup string, props map[string]any) string {
	reLoop := regexp.MustCompile(`(?s){for\slet\s(.*?)\s(of|in)\s(.*?)}(.*?){/for}`)
	matches := reLoop.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		full_match := match[0]
		for i, part := range match {
			if part == "of" {
				iterator := match[i-1]
				collection := match[i+1]
				result := match[i+2]
				full_result := ""
				collection_value := evalJS(collection, props)
				items := evaluateLoop(anyToString(collection_value))
				for _, value := range items {
					props[iterator] = value
					full_result += evalAllBrackets(result, props)
				}
				markup = strings.Replace(markup, full_match, full_result, 1)
				break
			}
			if part == "in" {
				result := match[i+1]
				markup = strings.Replace(markup, full_match, result, 1)
				break
			}
		}
		markup = strings.Replace(markup, full_match, "", 1) // Did not match any conditions, just remove it
	}
	return markup
}

func evaluateLoop(collection_value string) []string {
	vm := goja.New()
	v, err := vm.RunString(collection_value)
	if err != nil {
		log.Fatal(err)
	}
	var r []string
	err = vm.ExportTo(v, &r)
	if err != nil {
		log.Fatal(err)
	}
	return r
}

func getComponents(fence string) (string, []Component) {
	components := []Component{}
	reImport := regexp.MustCompile(`import\s+([A-Za-z_][A-Za-z_0-9]*)\s+from\s*"([^"]+)";`)
	for _, line := range strings.Split(fence, "\n") {
		match := reImport.FindStringSubmatch(line)
		if len(match) > 1 {
			compName := match[1]
			compPath := match[2]
			components = append(components, Component{
				Name: compName,
				Path: compPath,
			})
			fence = reImport.ReplaceAllString(fence, "") // Remove current import so script can run in goja
		}
	}
	return fence, components
}

func getCompArgs(comp_args []string, props map[string]any) (map[string]any, map[string]any) {
	comp_props := map[string]any{}
	comp_data := map[string]any{}
	for _, comp_arg := range comp_args {
		comp_arg = strings.TrimSpace(comp_arg)
		if strings.HasPrefix(comp_arg, "{") && strings.HasSuffix(comp_arg, "}") {
			prop_name := strings.Trim(comp_arg, "{}")
			prop_value := props[prop_name]
			comp_props[prop_name] = prop_value
			comp_data[prop_name] = prop_name
		}
		if strings.Contains(comp_arg, "={") && strings.HasSuffix(comp_arg, "}") {
			nameEndPos := strings.IndexRune(comp_arg, '=')
			prop_name := comp_arg[0:nameEndPos]

			valueStartPos := strings.IndexRune(comp_arg, '{')
			valueEndPos := strings.IndexRune(comp_arg, '}')
			prop_value := evalJS(comp_arg[valueStartPos+1:valueEndPos], props)

			comp_props[prop_name] = prop_value
			comp_data[prop_name] = comp_arg[valueStartPos+1 : valueEndPos]
		}
	}
	return comp_props, comp_data
}

func renderComponents(markup, script, style string, props map[string]any, components []Component) (string, string, string) {
	// Handle staticly imported components
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(markup, -1)
		for _, match := range matches {
			if len(match) > 1 {
				comp_args := strings.SplitAfter(match[1], "}")
				comp_props, comp_data := getCompArgs(comp_args, props)
				// Recursively render imports
				comp_markup, comp_script, comp_style := Render(component.Path, comp_props)
				// Create scoped classes and add to html
				comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup, comp_props, comp_data)
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
			comp_args := strings.SplitAfter(match[2], "}")
			comp_props, comp_data := getCompArgs(comp_args, props)
			comp_path = strings.Trim(comp_path, "\"'`") // Remove backticks, single and double quotes
			comp_markup, comp_script, comp_style := Render(comp_path, comp_props)
			// Create scoped classes and add to html
			comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup, comp_props, comp_data)
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
	var pairs []string
	for _, key := range val.MapKeys() {
		value := val.MapIndex(key)
		pairs = append(pairs, fmt.Sprintf("%s", key.Interface())+": "+anyToString(value.Interface()))
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

func makeGetter(comp_data map[string]any) string {
	//var comp_data_str string
	comp_data_str := "_fence: `age = age * 2;`," // TODO: fix temp hardcode

	params := make([]string, 0, len(comp_data))
	args := make([]string, 0, len(comp_data))
	for k, v := range comp_data {
		params = append(params, k)

		value_str := fmt.Sprintf("%s", v)
		value_str = strings.ReplaceAll(value_str, "'", "\\'") // escape single quotes
		value_str = strings.ReplaceAll(value_str, "\"", "'")  // change double quotes to single
		args = append(args, value_str)
	}
	params_str := strings.Join(params, ", ")
	args_str := strings.Join(args, ", ")

	i := 0
	//for name, value := range comp_data {
	for name := range comp_data {
		//value_str := fmt.Sprintf("%s", value)
		//value_str = strings.ReplaceAll(value_str, "'", "\\'") // escape single quotes
		//value_str = strings.ReplaceAll(value_str, "\"", "'")  // change double quotes to single
		//new_args := args
		//new_args[i] = value_str
		//args_str := strings.Join(new_args, ", ")
		comp_data_str += fmt.Sprintf("get %s() {return (new Function('%s', `${this._fence}; return %s;`))(%s); },", name, params_str, name, args_str)
		//comp_data_str += "get " + name + "() { return " + value_str + " },"
		i++
	}
	return "{" + comp_data_str + "}"
}

func isBoolAndTrue(value any) bool {
	if b, ok := value.(bool); ok && b {
		return true
	}
	return false
}

func main() {
	// Render the template with data
	props := map[string]any{"name": "John", "age": 2, "animals": []string{"cat", "dog", "pig"}}
	markup, script, style := Render("views/home.html", props)
	os.WriteFile("./public/script.js", []byte(script), fs.ModePerm)
	os.WriteFile("./public/style.css", []byte(style), fs.ModePerm)
	os.WriteFile("./public/index.html", []byte(markup), fs.ModePerm)

	http.Handle("/", http.FileServer(http.Dir("./public")))
	fmt.Println("visit site at: http://localhost:3000")
	http.ListenAndServe(":3000", nil)
}
