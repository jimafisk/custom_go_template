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
	// Replace any simple vars in the format {myProp} with the value
	markup = applyProps(markup, props)
	// Run template conditions {if}{else}{/if}
	markup = renderConditions(markup, props)
	// Run template loops {for let _ in _}{/for} and {for let _ of _}{/for}
	markup = renderLoops(markup, props)
	// Recursively render imported components
	markup, script, style = renderComponents(markup, script, style, props, components)
	// Create scoped classes and add to html
	markup, scopedElements := scopeHTML(markup)
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

func scopeHTML(markup string) (string, []scopedElement) {
	scopedElements := []scopedElement{}
	node, _ := html.Parse(strings.NewReader(markup))

	node, scopedElements = traverse(node, scopedElements)

	// Render the modified HTML back to a string
	buf := &strings.Builder{}
	err := html.Render(buf, node)
	if err != nil {
		log.Fatal(err)
	}
	markup = html.UnescapeString(buf.String())

	return markup, scopedElements
}

func scopeHTMLComp(comp_markup string) (string, []scopedElement) {
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
		node, scopedElements = traverse(node, scopedElements)

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

func traverse(node *html.Node, scopedElements []scopedElement) (*html.Node, []scopedElement) {
	var traverse func(*html.Node)
	traverse = func(node *html.Node) {
		if node.Type == html.ElementNode && node.DataAtom.String() != "" {
			tag := node.Data
			id := ""
			classes := []string{}
			scopedClass := getTagScopedClass(tag, scopedElements)

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
						}
					}
					if !alreadyScoped {
						node.Attr[i].Val += " " + scopedClass
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

type visitor struct{}

func (*visitor) Exit(js.INode) {}
func (v *visitor) Enter(node js.INode) js.IVisitor {
	switch node := node.(type) {
	case *js.Var:
		if node.Decl.String() == "LexicalDecl" && !strings.Contains(node.String(), "_plenti_") {
			randomStr, _ := generateRandom()
			node.Data = append(node.Data, []byte("_plenti_"+randomStr)...)
		}
	}
	return v
}

func scopeJS(script string, scopedElements []scopedElement) string {
	ast, _ := js.Parse(parse.NewInputString(script), js.Options{})
	v := visitor{}
	js.Walk(&v, ast)
	script = ast.JSString()
	return script
}

func getTagScopedClass(tag string, scopedElements []scopedElement) string {
	for _, elem := range scopedElements {
		if elem.tag == tag {
			return elem.scopedClass
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
		switch value := value.(type) {
		case string:
			fence = reProp.ReplaceAllString(fence, "let "+name+"='"+value+"';")
		case int:
			fence = reProp.ReplaceAllString(fence, "let "+name+"="+strconv.Itoa(value)+";")
		case int64:
			fence = reProp.ReplaceAllString(fence, "let "+name+"="+strconv.Itoa(int(value))+";")
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
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

func applyProps(markup string, props map[string]any) string {
	// Replace placeholders with data
	for name, value := range props {
		reTextNodesOnly := regexp.MustCompile(fmt.Sprintf(`(>.*?)({%s})(.*?<)`, name)) // TODO: Only temp replacing textnodes to avoid conflicts with props
		switch value := value.(type) {
		case string:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+value+`${3}`)
		case int:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+strconv.Itoa(value)+`${3}`)
		case int64:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+strconv.Itoa(int(value))+`${3}`)
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
	}
	return markup
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
				if evaluateCondition(condition, props) {
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

func evaluateCondition(condition string, props map[string]any) bool {
	vm := goja.New()
	l := js.NewLexer(parse.NewInputString(condition))
	for {
		tt, text := l.Next()
		switch tt {
		case js.ErrorToken:
			if l.Err() != io.EOF {
				fmt.Println("Error: ", l.Err())
			}
			result, err := vm.RunString(condition)
			if err != nil {
				fmt.Println("For condition: " + condition)
				log.Fatal(err)
			}
			return result.ToBoolean()
		case js.IdentifierToken:
			value, ok := props[string(text)]
			if ok {
				condition = strings.Replace(condition, string(text), anyToString(value), 1)
			}
		default:
			//fmt.Println("Token Type is: " + js.TokenType(tt).String())
		}
	}
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
				collection_value, ok := props[collection]
				if !ok {
					collection_value = collection
				}
				items := evaluateLoop(fmt.Sprintf("%v", collection_value)) // like anyToString() but doesn't wrap in quotes
				for _, value := range items {
					reLoopVar := regexp.MustCompile(`{` + iterator + `}`)
					evaluated_result := reLoopVar.ReplaceAllString(result, value)
					full_result = full_result + evaluated_result
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

func renderComponents(markup, script, style string, props map[string]any, components []Component) (string, string, string) {
	// Handle staticly imported components
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(markup, -1)
		for i, match := range matches {
			if len(match) > 1 {
				comp_props := map[string]any{}
				reProp := regexp.MustCompile(`{(.*?)}`)
				wrapped_props := reProp.FindAllStringSubmatch(match[1], -1)
				for _, wrapped_prop := range wrapped_props {
					prop_name := wrapped_prop[1]
					prop_value := props[prop_name]
					comp_props[prop_name] = prop_value
				}
				// Recursively render imports
				comp_markup, comp_script, comp_style := Render(component.Path, comp_props)
				// Create scoped classes and add to html
				comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup)
				// Add scoped classes to css
				comp_style, _ = scopeCSS(comp_style, comp_scopedElements)
				// Add scoped classes to js
				comp_script = scopeJS(comp_script, comp_scopedElements)

				// Replace only one component (in case multiple of the same comps are placed on the page)
				found := reComponent.FindString(markup)
				if found != "" {
					markup = strings.Replace(markup, found, comp_markup, 1)
				}
				if i < 1 {
					// Temp don't re-add script for comps already used
					// TODO: Need to scope JS vars to each comp
					script = script + comp_script
				}
				style = style + comp_style
			}
		}
	}
	// Handle dynamic components
	reDynamicComponent := regexp.MustCompile(`<=(".*?"|'.*?'|{.*?})\s({.*?})?(?:\s)?/>`)
	matches := reDynamicComponent.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		if len(match) >= 1 {
			var comp_path string
			wrapped_comp_path := match[1]
			if strings.HasPrefix(wrapped_comp_path, `"`) && strings.HasSuffix(wrapped_comp_path, `"`) {
				comp_path = strings.Trim(wrapped_comp_path, `"`)
			}
			if strings.HasPrefix(wrapped_comp_path, `'`) && strings.HasSuffix(wrapped_comp_path, `'`) {
				comp_path = strings.Trim(wrapped_comp_path, `'`)
			}
			if strings.HasPrefix(wrapped_comp_path, `{`) && strings.HasSuffix(wrapped_comp_path, `}`) {
				comp_path_var := strings.Trim(wrapped_comp_path, "{}")
				comp_path = fmt.Sprintf("%v", props[comp_path_var]) // Converts any to string, but doesn't wrap in quotes like anyToString()
			}
			comp_props := map[string]any{}
			if len(match) >= 2 && match[2] != "" {
				comp_props_str := match[2]
				prop_names := strings.Split(comp_props_str, " ")
				for _, wrapped_prop_name := range prop_names {
					prop_name := strings.Trim(wrapped_prop_name, "{}")
					comp_props[prop_name] = props[prop_name]
				}
			}
			comp_markup, comp_script, comp_style := Render(comp_path, comp_props)
			// Create scoped classes and add to html
			comp_markup, comp_scopedElements := scopeHTMLComp(comp_markup)
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

func anyToString(value any) string {
	switch value := value.(type) {
	case string:
		return "`" + value + "`"
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.Itoa(int(value))
	default:
		fmt.Println(reflect.TypeOf(value))
		return ""
	}
}

func main() {
	// Render the template with data
	props := map[string]any{"name": "John", "age": 2}
	markup, script, style := Render("views/home.html", props)
	os.WriteFile("./public/script.js", []byte(script), fs.ModePerm)
	os.WriteFile("./public/style.css", []byte(style), fs.ModePerm)
	os.WriteFile("./public/index.html", []byte(markup), fs.ModePerm)

	http.Handle("/", http.FileServer(http.Dir("./public")))
	fmt.Println("visit site at: http://localhost:3000")
	http.ListenAndServe(":3000", nil)
}
